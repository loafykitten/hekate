"""Failure-path checks for hash-bound records and recoverable promotion."""
import os
from pathlib import Path
P=Path(__file__).resolve().parent;os.environ['TMPDIR']=str(P/'tmp')
import sys,tempfile
sys.dont_write_bytecode=True;tempfile.tempdir=str(P/'tmp');sys.path.insert(0,str(P))
import unittest,json,shutil
from unittest.mock import patch
import delivery
import build_nukorio as b
import animate_nukorio as a

class Promotion(unittest.TestCase):
 def setUp(self):
  self.root=P/'tmp/failure-tests'
  if self.root.exists():shutil.rmtree(self.root)
  self.root.mkdir();self.tmp=self.root/'tmp';self.tmp.mkdir();self.out=self.root/'out';self.out.mkdir();(self.out/'data').write_text('old');self.stage=self.root/'stage';self.stage.mkdir();(self.stage/'data').write_text('new')
 def test_second_rename_failure(self):
  original=Path.rename
  def rename(p,dest):
   if p==self.tmp/'promotion':raise OSError('injected second rename failure')
   return original(p,dest)
  with patch.object(Path,'rename',rename):
   with self.assertRaises(OSError):delivery.promote(self.stage,self.out,self.tmp,lambda p:None)
  self.assertEqual((self.out/'data').read_text(),'old')
 def test_interrupted_promotion(self):
  self.out.rename(self.tmp/'previous-delivery');(self.tmp/'promotion-state.json').write_text('{"phase":"moving"}')
  delivery.recover(self.out,self.tmp);self.assertEqual((self.out/'data').read_text(),'old')
 def test_post_promotion_validation_failure(self):
  def validate(p):
   if p==self.out:raise ValueError('injected validation failure')
  with self.assertRaises(ValueError):delivery.promote(self.stage,self.out,self.tmp,validate)
  self.assertEqual((self.out/'data').read_text(),'old')
 def test_success(self):
  delivery.promote(self.stage,self.out,self.tmp,lambda p:None)
  self.assertEqual((self.out/'data').read_text(),'new');self.assertEqual((self.tmp/'previous-delivery/data').read_text(),'old')

class CachedVerification(unittest.TestCase):
 def test_changed_glb(self):
  p=b.STAGE/'renamon-phone.glb';original=p.read_bytes()
  try:
   p.write_bytes(original+b'\0')
   with self.assertRaisesRegex(AssertionError,'Stale verification'):b.manifest()
  finally:p.write_bytes(original)
 def test_changed_input_binding(self):
  p=b.TMP/'verified-desktop.json';original=p.read_bytes();record=json.loads(original);record['input_hashes']['Idle.fbx']='0'*64
  try:
   b.write_json(p,record)
   with self.assertRaisesRegex(AssertionError,'Stale input'):b.manifest()
  finally:p.write_bytes(original)
 def test_mixamo_mismatch(self):
  p=P/'mixamo-inputs.json';original=p.read_bytes();record=json.loads(original);record['Idle.fbx']='0'*64
  try:
   b.write_json(p,record)
   with self.assertRaisesRegex(AssertionError,'Mixamo input mismatch'):a.inputs()
  finally:p.write_bytes(original)

class UVCalibration(unittest.TestCase):
 def setUp(self):
  import struct
  self.path=P/'tmp/uv-calibration-test.glb'
  self.original=(P/'tmp/round2/renamon-phone.glb').read_bytes()
  cal=json.loads((P/'round2-uv-calibration.json').read_text())
  self.start=28+struct.unpack_from('<I',self.original,12)[0]+cal['buffer_view_spec']['byteOffset']
 def tearDown(self):
  if self.path.exists():self.path.unlink()
 def test_tiny_uv_drift_restored_exactly(self):
  import struct
  raw=bytearray(self.original);value=struct.unpack_from('<f',raw,self.start)[0];struct.pack_into('<f',raw,self.start,value+1e-6);self.path.write_bytes(raw)
  b.calibrate_round2_uv(self.path,'phone');self.assertEqual(self.path.read_bytes(),self.original)
 def test_large_uv_drift_rejected(self):
  import struct
  raw=bytearray(self.original);value=struct.unpack_from('<f',raw,self.start)[0];struct.pack_into('<f',raw,self.start,value+.01);self.path.write_bytes(raw)
  with self.assertRaisesRegex(AssertionError,'UV calibration drift'):b.calibrate_round2_uv(self.path,'phone')
 def test_non_uv_drift_rejected(self):
  raw=bytearray(self.original);raw[-1]^=1;self.path.write_bytes(raw)
  with self.assertRaisesRegex(AssertionError,'Non-UV'):b.calibrate_round2_uv(self.path,'phone')

if __name__=='__main__':unittest.main(argv=['test_pipeline'],verbosity=2)
