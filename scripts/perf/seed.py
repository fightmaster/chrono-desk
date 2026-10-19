# Task: CHR-DESK-PERF-001; Docs-Impact: LOCAL
# Only approved sanitized fixtures; never point the probe at a working event.
from pathlib import Path
import argparse
import datetime
import hashlib
import json
import os
import sqlite3

os.umask(0o077)
parser = argparse.ArgumentParser(description='Seed NEW diagnostic databases from an approved sanitized timing fixture')
parser.add_argument('fixture', type=Path)
parser.add_argument('directory', type=Path)
args = parser.parse_args()
root = args.directory.resolve()
root.mkdir(mode=0o700, parents=True, exist_ok=True)
if root.stat().st_mode & 0o077:
    raise RuntimeError('Private directory required')
source = args.fixture.resolve()
fixture = json.loads(source.read_text())
if fixture.get('schema') != 'sanitized-timing-performance-copy-v1':
    raise RuntimeError('Approved sanitized fixture required')
event_id = str(fixture['event_id'])
if not event_id or any((c not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-' for c in event_id)):
    raise RuntimeError('Unsafe event filename')
schema = (Path(__file__).resolve().parents[2] / 'internal/infrastructure/sqlite/schema.sql').read_text()

def ms(v):
    if v is None:
        return None
    if isinstance(v, (int, float)):
        return int(v)
    d = datetime.datetime.fromisoformat(v.replace('Z', '+00:00'))
    if d.tzinfo is None:
        d = d.replace(tzinfo=datetime.timezone.utc)
    return round(d.timestamp() * 1000)

def insert(db, table, rows):
    for row in rows:
        keys = list(row)
        db.execute('INSERT INTO ' + table + ' (' + ','.join(keys) + ') VALUES (' + ','.join(('?' for _ in keys)) + ')', [row[k] for k in keys])
for mode in ['single', 'separate']:
    run = root / mode
    run.mkdir(mode=0o700, exist_ok=True)
    path = run / (event_id + '.chrono')
    if path.exists():
        raise RuntimeError('Keep existing benchmark evidence; refuse overwrite')
    db = sqlite3.connect(path)
    db.executescript(schema)
    db.execute('PRAGMA foreign_keys=ON')
    with db:
        insert(db, 'events', [{'id': event_id, 'name': 'Sanitized performance copy', 'timezone': 'Europe/Moscow'}])
        cats = sorted({str(m['category_id']) for m in fixture['tables']['members'] if m['category_id'] is not None})
        insert(db, 'categories', [{'id': v, 'name': 'Group ' + v} for v in cats])
        insert(db, 'races', [{'id': str(r['id']), 'event_id': event_id, 'name': 'Race ' + str(r['id']), 'started_at_ms': ms(r['started_at']), 'format': r['format'], 'time_limit_seconds': r['time_limit_seconds'], 'category_excludes_top_by_gender': int(r['category_excludes_top_by_gender'])} for r in fixture['tables']['races']])
        insert(db, 'checkpoints', [{'id': str(c['id']), 'event_id': event_id, 'race_id': str(c['race_id']), 'name': 'Checkpoint ' + str(c['id']), 'type': c['type'], 'sort': c['sort'], 'board': c['board'], 'since_ms': ms(c['since']), 'since_offset_seconds': c['since_offset_seconds'], 'sleep_after_prev_seconds': c['sleep_after_prev_seconds']} for c in fixture['tables']['checkpoints']])
        rows = []
        for m in fixture['tables']['members']:
            row = {k: m.get(k) for k in ['number', 'epc', 'start_time_source', 'start_observation_id', 'clean_time']}
            row.update(id=str(m['id']), event_id=event_id, race_id=str(m['race_id']), category_id=str(m['category_id']) if m['category_id'] is not None else None, status=m['status'] or 0, gender=m['gender'] if isinstance(m['gender'], str) else {1: 'male', 2: 'female'}.get(m['gender']), first_name='Participant', last_name=str(m['id']), start_time_ms=ms(m['start_time']), finish_time_ms=ms(m['finish_time']))
            rows.append(row)
        insert(db, 'members', rows)
        rows = []
        for r in fixture['tables']['rfid_logs']:
            row = {k: r[k] for k in ['id', 'status', 'number', 'ant', 'epc', 'rssi', 'board']}
            row.update(event_id=event_id, time_ms=r['time'], disabled_at=ms(r['disabled_at']))
            rows.append(row)
        insert(db, 'rfid_logs', rows)
        rows = []
        for r in fixture['tables']['results']:
            row = {k: r[k] for k in ['id', 'rfid_log_id', 'time_ms', 'number']}
            row.update(event_id=event_id, race_id=str(r['race_id']), member_id=str(r['member_id']), checkpoint_id=str(r['checkpoint_id']) if r['checkpoint_id'] is not None else None)
            rows.append(row)
        insert(db, 'results', rows)
    assert db.execute('PRAGMA foreign_key_check').fetchall() == []
    assert db.execute('PRAGMA integrity_check').fetchone() == ('ok',)
    member = db.execute('SELECT m.id,m.race_id,count(*) n FROM members m JOIN rfid_logs l ON l.event_id=m.event_id AND l.epc=m.epc GROUP BY m.id ORDER BY n DESC LIMIT 1').fetchone()
    metadata = {'sanitized_source_sha256': hashlib.sha256(source.read_bytes()).hexdigest(), 'rows': {k: len(v) for k, v in fixture['tables'].items()}, 'member': member[0], 'race': member[1], 'mode': mode, 'event': event_id}
    (run / 'seed-metadata.json').write_text(json.dumps(metadata) + '\n')
    db.close()
    path.chmod(0o600)
print('Two new sanitized copies seeded; input fixture unchanged.')
