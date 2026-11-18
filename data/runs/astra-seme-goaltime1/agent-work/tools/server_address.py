import os
from pathlib import Path
primary='s2'
p=Path(__file__).resolve().parent.parent/'config'/'primary.env'
if p.exists():
 for line in p.read_text().splitlines():
  if line.startswith('ISU_PRIMARY='):primary=line.split('=',1)[1].strip()
API_ADDRESS=os.environ.get('ISU_API_ADDRESS',{'s1':'10.99.0.11','s2':'10.99.0.12','s3':'10.99.0.13'}[primary])
