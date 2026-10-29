"""Bounded observations of this process and its execution workspace.

Sampling observes the existing allocator, native stages and cache files. It does
not install a memory pool, change a dataset call or retain per-sample records.
"""
from contextlib import contextmanager
from datetime import datetime, timezone
import os
from pathlib import Path
import resource
import stat
import threading
import time

PHASES = frozenset(('input_validate', 'load_convert', 'truncate', 'tokenize', 'model_load',
                    'optimizer', 'adapter_save', 'export', 'verify'))
INTERVAL = 0.1
MAX_VISIBLE_FILES = 128


def _integer_file(path):
    raw = path.read_text().strip()
    return raw if raw == 'max' else int(raw)


def sample_process():
    result = {'rss_bytes': None, 'arrow_allocated_bytes': None,
              'process_lifetime_rss_peak_bytes': resource.getrusage(resource.RUSAGE_SELF).ru_maxrss * 1024,
              'arrow_pool_lifetime_peak_bytes': None, 'cgroup': {}, 'unavailable': {}}
    try:
        with Path('/proc/self/statm').open() as stream:
            result['rss_bytes'] = int(stream.read(1024).split()[1]) * os.sysconf('SC_PAGE_SIZE')
    except (OSError, ValueError, IndexError) as error:
        result['unavailable']['rss'] = type(error).__name__
    try:
        import pyarrow
        pool = pyarrow.default_memory_pool()
        result['arrow_allocated_bytes'] = pool.bytes_allocated()
        result['arrow_pool_lifetime_peak_bytes'] = pool.max_memory()
    except (ImportError, AttributeError, RuntimeError) as error:
        result['unavailable']['arrow'] = type(error).__name__
    root = Path('/sys/fs/cgroup')
    for key, filename in (('current_bytes', 'memory.current'), ('lifetime_peak_bytes', 'memory.peak'), ('limit_bytes', 'memory.max')):
        try:
            result['cgroup'][key] = _integer_file(root / filename)
        except (OSError, ValueError) as error:
            result['unavailable'][filename] = type(error).__name__
    for filename in ('memory.stat', 'memory.events'):
        try:
            values = dict(line.split() for line in (root / filename).read_text().splitlines())
            if filename == 'memory.stat':
                for key in ('anon', 'file'):
                    result['cgroup'][key + '_bytes'] = int(values[key])
            else:
                result['cgroup']['events'] = {key: int(values[key]) for key in ('oom', 'oom_kill', 'max') if key in values}
        except (OSError, ValueError, KeyError) as error:
            result['unavailable'][filename] = type(error).__name__
    return result


def _bytes(value):
    return str(value) if isinstance(value, int) and value >= 0 else (value if value == 'max' else None)


class PhaseMeasurements:
    def __init__(self, workspace, role, *, sampler=sample_process):
        self.workspace = Path(workspace).resolve()
        self.role = role
        self.work = self.workspace / 'work' / role
        self.sampler = sampler
        self.phases = []
        self.cache_files = {}
        self.active = None
        self.lock = threading.Lock()

    def _disk(self):
        logical = allocated = count = inaccessible = 0
        visible = []
        arrow_count = 0
        allocated_inodes = set()
        for directory, dirs, files in os.walk(self.workspace, followlinks=False):
            dirs[:] = sorted(name for name in dirs if not (Path(directory) / name).is_symlink())
            for name in sorted(files):
                path = Path(directory) / name
                try:
                    info = path.lstat()
                    if not stat.S_ISREG(info.st_mode):
                        continue
                    logical += info.st_size
                    inode = (info.st_dev, info.st_ino)
                    if inode not in allocated_inodes:
                        allocated += info.st_blocks * 512
                        allocated_inodes.add(inode)
                    count += 1
                    if path.suffix == '.arrow' and path.is_relative_to(self.work):
                        arrow_count += 1
                        if len(visible) < MAX_VISIBLE_FILES:
                            visible.append({'path': str(path), 'size_bytes': str(info.st_size)})
                except OSError:
                    inaccessible += 1
        result = {'workdir_logical_bytes': str(logical), 'workdir_allocated_bytes': str(allocated),
                  'regular_file_count': count, 'inaccessible_file_count': inaccessible,
                  'workdir_scope': 'regular-file logical bytes and inode-deduplicated allocated blocks; filesystem metadata excluded',
                  'visible_arrow_files': visible, 'visible_arrow_file_count': arrow_count,
                  'visible_arrow_files_omitted': max(0, arrow_count - len(visible)),
                  'visible_arrow_origin': 'workspace enumeration, not source Dataset.cache_files'}
        try:
            fs = os.statvfs(self.workspace)
            result['filesystem_available_bytes'] = str(fs.f_bavail * fs.f_frsize)
            result['filesystem_total_bytes'] = str(fs.f_blocks * fs.f_frsize)
            result['filesystem_scope'] = 'mounted filesystem; not requested PVC capacity'
        except OSError as error:
            result['filesystem_unavailable'] = type(error).__name__
        return result

    def record_dataset_cache_files(self, stage, paths):
        if stage not in ('aligned', 'tokenized'):
            raise ValueError('unknown native dataset cache stage')
        result = []
        total = 0
        for value in paths:
            path = Path(value)
            if path.is_symlink() or not path.is_file() or not path.resolve().is_relative_to(self.work):
                raise ValueError('native cache escaped the execution role')
            total += 1
            if len(result) < MAX_VISIBLE_FILES:
                result.append({'path': str(path.resolve()), 'size_bytes': str(path.stat().st_size)})
        self.cache_files[stage] = result
        self.cache_files[stage + '_omitted'] = max(0, total - len(result))

    def sample(self):
        started = time.monotonic()
        value = self.sampler()
        observed_at = time.monotonic()
        with self.lock:
            record = self.active
            if record is None:
                return
            record['sample_count'] += 1
            if '_last_sample_at' in record:
                record['max_observed_sample_gap_seconds'] = max(record.get('max_observed_sample_gap_seconds', 0), observed_at - record['_last_sample_at'])
            record['_last_sample_at'] = observed_at
            record['max_sample_duration_seconds'] = max(record.get('max_sample_duration_seconds', 0), observed_at - started)
            for source, target in (('rss_bytes', 'rss_sample_peak_bytes'), ('arrow_allocated_bytes', 'arrow_sample_peak_bytes'),
                                   ('process_lifetime_rss_peak_bytes', 'process_lifetime_rss_peak_bytes'),
                                   ('arrow_pool_lifetime_peak_bytes', 'arrow_pool_lifetime_peak_bytes')):
                current = value.get(source)
                if isinstance(current, int) and current >= 0:
                    record[target] = max(record.get(target, 0), current)
            group = value.get('cgroup', {})
            for source, target in (('current_bytes', 'current_sample_peak_bytes'), ('anon_bytes', 'anon_sample_peak_bytes'), ('file_bytes', 'file_sample_peak_bytes')):
                current = group.get(source)
                if isinstance(current, int) and current >= 0:
                    record['cgroup'][target] = max(record['cgroup'].get(target, 0), current)
            record['cgroup'].setdefault('lifetime_peak_start_bytes', group.get('lifetime_peak_bytes'))
            record['cgroup']['lifetime_peak_end_bytes'] = group.get('lifetime_peak_bytes')
            record['cgroup']['limit_bytes'] = group.get('limit_bytes')
            record['cgroup'].setdefault('events_start', group.get('events', {}))
            record['cgroup']['events_end'] = group.get('events', {})
            record['unavailable'].update(value.get('unavailable', {}))

    @contextmanager
    def phase(self, name):
        if name not in PHASES or any(p['name'] == name for p in self.phases) or self.active is not None:
            raise ValueError('invalid or repeated measurement phase')
        started = time.monotonic()
        record = {'name': name, 'started_at': datetime.now(timezone.utc).isoformat(),
                  'started_monotonic_seconds': started, 'sample_count': 0,
                  'cgroup': {}, 'unavailable': {}, 'before': self._disk(), 'outcome': 'FAILED'}
        self.phases.append(record)
        self.active = record
        stopped = threading.Event()

        def observe():
            while not stopped.wait(INTERVAL):
                self.sample()

        self.sample()
        thread = threading.Thread(target=observe, name='phase-measurement', daemon=True)
        thread.start()
        try:
            yield
            record['outcome'] = 'SUCCEEDED'
        except BaseException as error:
            record['error_class'] = type(error).__name__[:64]
            raise
        finally:
            stopped.set()
            thread.join()
            self.sample()
            record['elapsed_seconds'] = time.monotonic() - started
            record['ended_at'] = datetime.now(timezone.utc).isoformat()
            record['after'] = self._disk()
            self.active = None

    def report(self):
        records = []
        for phase in self.phases:
            value = {key: item for key, item in phase.items() if not key.startswith('_')}
            for field in ('rss_sample_peak_bytes', 'arrow_sample_peak_bytes', 'process_lifetime_rss_peak_bytes', 'arrow_pool_lifetime_peak_bytes'):
                value[field] = _bytes(phase.get(field))
            value['cgroup'] = {key: (_bytes(item) if key.endswith('_bytes') else item) for key, item in phase['cgroup'].items()}
            records.append(value)
        return {'schema': 'ani.finetuning.phase-measurements.v1', 'role': self.role,
                'sample_interval_seconds': '0.1', 'peak_semantics': 'phase sampled peaks; process/allocator/cgroup lifetime high-water marks are separate and never reset',
                'workspace_path': str(self.workspace), 'role_work_path': str(self.work),
                'phases': records, 'dataset_cache_files': dict(self.cache_files),
                'final_workspace': self._disk()}
