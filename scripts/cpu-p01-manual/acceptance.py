"""Real CPU-P01 BFF client, run in a restricted target-cluster Job only."""
import hashlib
import json
import math
from pathlib import Path
import ssl
import sys
import time
import urllib.error
import urllib.request
import uuid

class API:
    def __init__(self, base, token):
        self.base = base.rstrip('/')
        self.token = Path(token).read_text().strip()
        self.deadline = None

    def call(self, method, path, body=None, expected=(200,), extra_headers=None):
        headers = {'Authorization': 'Bearer '+self.token, 'Content-Type': 'application/json'}
        headers.update(extra_headers or {})
        raw = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.base+path, raw, headers, method=method)
        timeout = 35
        if self.deadline is not None:
            timeout = min(timeout, self.deadline-time.monotonic())
            if timeout <= 0:
                raise RuntimeError('real execution stop observation deadline exceeded')
        try:
            with urllib.request.urlopen(request, timeout=timeout) as response:
                assert response.status in expected, 'unexpected BFF status'
                return json.loads(response.read(2 << 20) or b'{}')
        except urllib.error.HTTPError as error:
            if error.code in expected:
                result = {'http_status': error.code}
                try:
                    peer = json.loads(error.read(8192))
                    if isinstance(peer, dict) and isinstance(peer.get('reason'), str):
                        result['reason'] = peer['reason']
                except (ValueError, OSError):
                    pass
                error.close()
                return result
            error.close()
            raise RuntimeError('BFF '+method+' '+path.split('?')[0]+' HTTP '+str(error.code)) from None

def emit(value):
    print(json.dumps(value, separators=(',', ':')), flush=True)

def execution_view(response):
    """BFF returns the execution flat; older projections nest it under 'execution'."""
    view = response.get('execution') if isinstance(response, dict) else None
    return view if isinstance(view, dict) else response


def stop_training(api, config):
    """One authorized BFF client observes real optimizer output before Stop."""
    started = time.monotonic()
    deadline = started+900
    api.deadline = deadline
    accepted = api.call('POST', '/admin/v1/modeldev/executions', config['request'], expected=(202,))
    emit({'event': 'accepted', 'receipt': accepted})
    execution, operation = accepted['execution_id'], accepted['operation_id']
    for value in (execution, operation):
        assert str(uuid.UUID(value)) == value
    replay = api.call('POST', '/admin/v1/modeldev/executions', config['request'], expected=(202,))
    assert replay['execution_id'] == execution and replay['operation_id'] == operation and replay['replayed']
    emit({'event': 'replay', 'receipt': replay})
    base = '/admin/v1/modeldev/executions/'+execution
    stopped = None
    log_id = None
    step_evidence = None
    while time.monotonic() < deadline:
        response = api.call('GET', base, expected=(200, 404))
        if response.get('http_status') == 404:
            emit({'event': 'awaiting-projection', 'execution_id': execution, 'http_status': 404})
            time.sleep(min(0.5, max(0, deadline-time.monotonic())))
            continue
        view = execution_view(response)
        assert view['execution_id'] == execution and view['operation_id'] == operation
        emit({'event': 'observed', 'execution': view})
        compute, close = view.get('compute_state'), view.get('close_state')
        if close == 'NEEDS_REVIEW' or view.get('delivery_state') == 'PUBLISHED' or compute == 'SUCCEEDED':
            raise RuntimeError('real execution stop missed running training or requires review')
        if stopped is not None:
            if close == 'CLOSED':
                assert view['stop_requested'] and int(view['close_generation']) >= 1
                emit({'event': 'TRAINING_STOP_CLOSED_OBSERVED', 'execution_id': execution,
                      'operation_id': operation, 'execution': view, 'stop_receipt': stopped,
                      'step_evidence': step_evidence, 'result': 'NOT_VERIFIED',
                      'next': 'root verify actual PG USER_STOP, original KFP canceled Run and writer absence'})
                return
        else:
            if compute in ('FAILED', 'CANCELED', 'CANCELLED') or close == 'CLOSED':
                raise RuntimeError('real execution reached terminal state before observed training Stop')
            # No training-state label can replace an actual optimizer metric.
            logs = api.call('GET', base+'/logs?tail_lines=100&max_bytes=32768', expected=(200, 404, 503))
            logs_received_at = time.monotonic()
            if logs.get('http_status') in (404, 503):
                if logs['http_status'] == 503 and logs.get('reason') != 'MODELDEV_QUERY_UNAVAILABLE':
                    raise RuntimeError('real execution unexpected training log error')
                emit({'event': 'awaiting-training-logs', 'execution_id': execution,
                      'http_status': logs['http_status'], 'reason': logs.get('reason')})
            else:
                assert logs.get('observed_at') and str(uuid.UUID(logs['log_id'])) == logs['log_id']
                if log_id is None:
                    log_id = logs['log_id']
                assert logs['log_id'] == log_id, 'training log segment changed'
                metrics = []
                for line in logs.get('lines', []):
                    text = line['text']
                    if text.startswith('CPU training failed:'):
                        raise RuntimeError('real execution trainer reported terminal error')
                    try:
                        metric = json.loads(text)
                    except ValueError:
                        continue
                    if not isinstance(metric, dict) or metric.get('schema') != 'ani.metric.v1':
                        continue
                    assert metric.get('name') == 'train.loss' and metric.get('rank') == 0
                    assert type(metric.get('step')) is int and 1 <= metric['step'] <= 48
                    assert type(metric.get('value')) in (int, float) and math.isfinite(metric['value'])
                    assert metric.get('timestamp') and line.get('timestamp')
                    metrics.append({'timestamp': line['timestamp'], 'metric': metric})
                if metrics and max(item['metric']['step'] for item in metrics) >= 3:
                    assert max(item['metric']['step'] for item in metrics) < 48, 'training already completed'
                    step_evidence = {'log_id': log_id, 'observed_at': logs['observed_at'],
                                     'truncated': logs.get('truncated', False), 'metrics': metrics}
                    emit({'event': 'training-steps-observed', 'execution_id': execution,
                          'operation_id': operation, 'step_evidence': step_evidence, 'result': 'NOT_VERIFIED'})
                    # Stop immediately after the actual log read, in this same
                    # Job; do not wait for a second Job or another query.
                    api.deadline = min(started+1100, logs_received_at+1)
                    stopped = api.call('POST', base+':stop', expected=(202,))
                    stop_latency = time.monotonic()-logs_received_at
                    emit({'event': 'stop-accepted', 'receipt': stopped,
                          'seconds_from_step_observation_to_stop_receipt': stop_latency})
                    assert stop_latency < 1, 'Stop receipt arrived too late after actual metric'
                    deadline = min(started+1100, time.monotonic()+300)
                    api.deadline = deadline
                    replay_stop = api.call('POST', base+':stop', expected=(202,))
                    for receipt in (stopped, replay_stop):
                        assert receipt['execution_id'] == execution and receipt['operation_id'] == operation
                        assert receipt['stop_requested'] and int(receipt['intent_generation']) >= 1
                    assert replay_stop['replayed'] and replay_stop['intent_generation'] == stopped['intent_generation']
                    emit({'event': 'training-steps-stop-accepted', 'execution_id': execution,
                          'operation_id': operation, 'step_evidence': step_evidence,
                          'receipt': stopped, 'replay': replay_stop,
                          'seconds_from_step_observation_to_stop_receipt': stop_latency, 'result': 'NOT_VERIFIED'})
        time.sleep(min(0.5, max(0, deadline-time.monotonic())))
    if stopped is not None:
        raise RuntimeError('real execution stop did not reach CLOSED within 300s and bounded Job deadline')
    raise RuntimeError('real execution did not expose actual training steps within 900s')

def main():
    config = json.loads(Path(sys.argv[1]).read_text())
    api = API(config['base_url'], config['token_file'])
    mode = config['mode']
    if mode == 'stop_training':
        stop_training(api, config)
        return
    if mode == 'checks':
        for check in config['checks']:
            expected = tuple(check['expected'])
            result = api.call(check['method'], check['path'], check.get('body'), expected,
                              check.get('headers'))
            if 'visible_execution_ids' in check:
                actual = {item['execution_id'] for item in result.get('executions', [])}
                assert actual == set(check['visible_execution_ids']), 'unexpected visible executions'
            if 'execution_id' in check:
                assert execution_view(result)['execution_id'] == check['execution_id']
            emit({'event': 'check-PASS', 'name': check['name'],
                  'http_status': result.get('http_status', expected[0])})
        emit({'event': 'checks-PASS', 'count': len(config['checks'])})
        return
    if mode in ('create', 'follow'):
        if mode == 'create':
            accepted = api.call('POST', '/admin/v1/modeldev/executions', config['request'], expected=(202,))
            emit({'event': 'accepted', 'receipt': accepted})
            replay = api.call('POST', '/admin/v1/modeldev/executions', config['request'], expected=(202,))
            assert accepted['execution_id'] == replay['execution_id'] and replay['replayed']
            emit({'event': 'replay', 'receipt': replay})
            execution_id = accepted['execution_id']
        else:
            execution_id = config['execution_id']
            emit({'event': 'follow-existing', 'execution_id': execution_id})
        if config.get('wait', True):
            limit = time.monotonic()+config.get('wait_seconds', 900)
            while time.monotonic() < limit:
                response = api.call('GET', '/admin/v1/modeldev/executions/'+execution_id, expected=(200,404))
                if response.get('http_status') == 404:
                    emit({'event': 'awaiting-projection', 'execution_id': execution_id, 'http_status': 404})
                    time.sleep(5)
                    continue
                view = execution_view(response)
                emit({'event': 'observed', 'execution': view})
                if view.get('delivery_state') == 'PUBLISHED' and view.get('close_state') == 'CLOSED':
                    break
                if view.get('compute_state') in ('FAILED', 'CANCELED', 'CANCELLED') or view.get('close_state') in ('NEEDS_REVIEW', 'CLOSED'):
                    raise RuntimeError('real execution reached '+view.get('compute_state', '')+'/'+view.get('close_state', ''))
                time.sleep(5)
            else:
                raise RuntimeError('real execution did not reach PUBLISHED/CLOSED within bounded observation')
            logs = api.call('GET', '/admin/v1/modeldev/executions/'+execution_id+'/logs?tail_lines=100&max_bytes=32768')
            assert logs.get('lines') and logs.get('observed_at')
            emit({'event': 'training-logs', 'result': logs})
            artifacts = api.call('GET', '/admin/v1/modeldev/executions/'+execution_id+'/artifacts')
            assert {a['filename'] for a in artifacts['artifacts']} == {'model.pt', 'model_config.json', 'metrics.jsonl', 'summary.json'}
            emit({'event': 'published-artifacts', 'result': artifacts})
            emit({'event': 'L3_PASS', 'execution_id': execution_id})
        return
    execution_id = config['execution_id']
    if mode == 'stop':
        stopped = api.call('POST', '/admin/v1/modeldev/executions/'+execution_id+':stop', expected=(202,))
        emit({'event': 'stop-accepted', 'receipt': stopped})
        return
    if mode == 'observe':
        emit({'event': 'observed', 'result': api.call('GET', '/admin/v1/modeldev/executions/'+execution_id)})
        return
    if mode != 'verify':
        raise ValueError('unrecognized bounded acceptance mode')
    # This Job has no PVC/hostPath and no S3 credential. All object access is
    # through grants returned after current BFF authorization.
    artifacts = api.call('GET', '/admin/v1/modeldev/executions/'+execution_id+'/artifacts')['artifacts']
    assert len(artifacts) == 4
    directory = Path('/tmp/checkpoint')
    directory.mkdir(mode=0o700, exist_ok=False)
    tls = ssl.create_default_context(cafile=config['s3_ca_file'])
    names = set()
    for artifact in artifacts:
        name = artifact['filename']
        assert name in {'model.pt', 'model_config.json', 'metrics.jsonl', 'summary.json'} and name not in names
        names.add(name)
        grant = api.call('GET', '/admin/v1/modeldev/artifacts/'+artifact['artifact_id']+'/content')
        assert grant['artifact']['sha256'] == artifact['sha256'] and grant.get('expires_at')
        # Never log the signed URL, credentials, or peer error response.
        try:
            with urllib.request.urlopen(grant['download_url'], context=tls, timeout=30) as response:
                content = response.read((104857600)+1)
        except Exception:
            raise RuntimeError('authorized artifact byte download failed') from None
        assert len(content) == int(artifact['size_bytes']) and hashlib.sha256(content).hexdigest() == artifact['sha256']
        (directory/name).write_bytes(content)
        emit({'event': 'download-verified', 'filename': name, 'bytes': len(content), 'sha256': artifact['sha256']})
    summary = json.loads((directory/'summary.json').read_text())
    assert summary['epochs'] == 3 and summary['steps'] == 48 and summary['samples'] == 1024
    model_config = json.loads((directory/'model_config.json').read_text())
    assert model_config == {'architecture': 'mlp-16-32-2', 'input_dim': 16, 'hidden_dim': 32, 'output_dim': 2, 'dtype': 'float32'}
    import torch
    from torch import nn
    weights = torch.load(directory/'model.pt', map_location='cpu', weights_only=True)
    assert isinstance(weights, dict) and len(weights) == 4 and sum(t.numel() for t in weights.values()) == 610
    assert all(torch.is_tensor(t) and torch.isfinite(t).all() for t in weights.values())
    model = nn.Sequential(nn.Linear(16,32), nn.ReLU(), nn.Linear(32,2))
    model.load_state_dict(weights, strict=True)
    model.eval()
    with torch.inference_mode():
        result = model(torch.zeros((4,16),dtype=torch.float32))
    assert result.shape == (4,2) and torch.isfinite(result).all()
    emit({'event': 'L4_PASS', 'execution_id': execution_id, 'shape': list(result.shape), 'device': str(result.device),
          'epochs': 3, 'optimizer_steps': 48, 'artifact_count': 4})

if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        # No traceback can expose a signed URL or bearer token.
        message = str(error) if isinstance(error, RuntimeError) and str(error).startswith(('BFF ', 'real execution ', 'authorized artifact ')) else type(error).__name__
        emit({'event': 'FAIL', 'reason': message})
        sys.exit(1)
