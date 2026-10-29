"""Bounded normal-runtime phase statistics; no training substitutes."""
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'finetuning'))
from phase_measurement import PhaseMeasurements, sample_process


class PhaseMeasurementTest(unittest.TestCase):
    def test_phase_sample_peak_does_not_claim_lifetime_high_water(self):
        values = iter((10, 30, 20, 20, 20, 20))

        def sample():
            value = next(values, 20)
            return {'rss_bytes': value, 'arrow_allocated_bytes': value * 2,
                    'process_lifetime_rss_peak_bytes': 1000,
                    'arrow_pool_lifetime_peak_bytes': 2000,
                    'cgroup': {'current_bytes': value + 1, 'anon_bytes': value,
                               'file_bytes': 1, 'lifetime_peak_bytes': 3000,
                               'limit_bytes': 4096, 'events': {'oom': 0}}}

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            measurements = PhaseMeasurements(root, 'train', sampler=sample)
            with measurements.phase('load_convert'):
                measurements.sample()
                measurements.sample()
            report = measurements.report()
        phase = report['phases'][0]
        self.assertEqual(phase['rss_sample_peak_bytes'], '30')
        self.assertEqual(phase['arrow_sample_peak_bytes'], '60')
        self.assertEqual(phase['process_lifetime_rss_peak_bytes'], '1000')
        self.assertEqual(phase['arrow_pool_lifetime_peak_bytes'], '2000')
        self.assertEqual(phase['cgroup']['lifetime_peak_end_bytes'], '3000')
        self.assertEqual(phase['cgroup']['limit_bytes'], '4096')
        self.assertEqual(report['sample_interval_seconds'], '0.1')
        self.assertEqual(phase['outcome'], 'SUCCEEDED')

    def test_failure_retains_statistics_without_recording_exception_text(self):
        with tempfile.TemporaryDirectory() as directory:
            measurements = PhaseMeasurements(Path(directory), 'train')
            with self.assertRaises(ValueError):
                with measurements.phase('tokenize'):
                    raise ValueError('private input content must not become telemetry')
            report = measurements.report()
        self.assertEqual(report['phases'][0]['outcome'], 'FAILED')
        self.assertEqual(report['phases'][0]['error_class'], 'ValueError')
        self.assertNotIn('private input', json.dumps(report))

    def test_actual_cache_files_and_visible_cache_inventory_remain_distinct(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'work/train/hf/datasets').mkdir(parents=True)
            actual = root / 'work/train/hf/datasets/aligned.arrow'
            actual.write_bytes(b'actual disk cache')
            other = root / 'work/train/hf/datasets/source.arrow'
            other.write_bytes(b'visible native cache')
            measurements = PhaseMeasurements(root, 'train')
            measurements.record_dataset_cache_files('aligned', [str(actual)])
            with measurements.phase('load_convert'):
                pass
            report = measurements.report()
        self.assertEqual([x['path'] for x in report['dataset_cache_files']['aligned']], [str(actual)])
        visible = report['phases'][0]['after']['visible_arrow_files']
        self.assertEqual({x['path'] for x in visible}, {str(actual), str(other)})
        self.assertNotIn('source', report['dataset_cache_files'])
        self.assertGreater(int(report['phases'][0]['after']['workdir_logical_bytes']), 0)

    def test_statistics_are_aggregated_and_bounded(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            measurements = PhaseMeasurements(root, 'train')
            with measurements.phase('optimizer'):
                for _ in range(200):
                    measurements.sample()
            report = measurements.report()
        self.assertGreaterEqual(report['phases'][0]['sample_count'], 200)
        self.assertNotIn('samples', report['phases'][0])
        self.assertLess(len(json.dumps(report)), 16384)

    def test_workspace_allocated_bytes_do_not_double_count_hard_links(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            original = root / 'original'
            original.write_bytes(b'workspace payload')
            os.link(original, root / 'linked')
            measurements = PhaseMeasurements(root, 'train')
            disk = measurements.report()['final_workspace']
            self.assertEqual(int(disk['workdir_allocated_bytes']), original.stat().st_blocks * 512)
            self.assertEqual(int(disk['workdir_logical_bytes']), original.stat().st_size * 2)

    def test_real_process_rss_and_cgroup_unavailability_are_explicit(self):
        sample = sample_process()
        self.assertGreater(sample['rss_bytes'], 0)
        self.assertGreater(sample['process_lifetime_rss_peak_bytes'], 0)
        self.assertIn('cgroup', sample)
        self.assertIn('arrow_allocated_bytes', sample)
        if sample['arrow_allocated_bytes'] is None:
            self.assertIn('arrow', sample['unavailable'])


if __name__ == '__main__':
    unittest.main()
