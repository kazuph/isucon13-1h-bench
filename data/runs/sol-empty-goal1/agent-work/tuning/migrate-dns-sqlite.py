import sqlite3,subprocess,xml.etree.ElementTree as ET
from pathlib import Path
root=Path(__file__).parent
output=root/'isudns.sqlite3'
if output.exists(): raise RuntimeError('refusing to overwrite existing SQLite file')
db=sqlite3.connect(output)
db.executescript((root/'schema.sqlite3.sql').read_text())
for (table,) in db.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall():
 if not table.replace('_','').isalnum(): raise ValueError(table)
 column_data=subprocess.check_output(['ssh','s1',f'sudo mysql -N -e "SHOW COLUMNS FROM isudns.{table}"'],text=True)
 mysql_columns={line.split('\t')[0] for line in column_data.splitlines()}
 columns=[row[1] for row in db.execute(f'PRAGMA table_info({table})') if row[1] in mysql_columns]
 sql='SELECT '+','.join('`'+column+'`' for column in columns)+' FROM isudns.'+table
 document=ET.fromstring(subprocess.check_output(['ssh','s1','sudo mysql --xml -e \''+sql+'\'']))
 rows=[]
 for row in document.findall('row'):
  rows.append(tuple(None if field.get('{http://www.w3.org/2001/XMLSchema-instance}nil')=='true' else (field.text or '') for field in row.findall('field')))
 placeholders=','.join('?' for _ in columns)
 db.executemany(f'INSERT INTO {table} ({",".join(columns)}) VALUES ({placeholders})',rows)
 print(table,len(rows),'rows migrated')
db.commit()
assert db.execute('PRAGMA integrity_check').fetchone()==('ok',)
db.close()
