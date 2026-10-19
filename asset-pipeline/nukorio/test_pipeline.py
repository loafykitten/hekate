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

class GestureInputs(unittest.TestCase):
 def check_bad_pin(self,key,message):
  p=P/'mixamo-inputs.json';original=p.read_bytes();record=json.loads(original);record[key]='0'*64
  try:
   b.write_json(p,record)
   with self.assertRaisesRegex(AssertionError,message):a.inputs()
  finally:p.write_bytes(original)
 def test_pack_mismatch(self):self.check_bad_pin(a.PACK,'Mixamo input mismatch')
 def test_member_mismatch(self):self.check_bad_pin(a.PACK+'!head nod yes.fbx','Mixamo member mismatch')
 def test_xbot_mismatch(self):self.check_bad_pin(a.PACK+'!X Bot.fbx','Mixamo member mismatch')
 def test_missing_member_pin(self):
  p=P/'mixamo-inputs.json';original=p.read_bytes();record=json.loads(original);del record[a.PACK+'!weight shift.fbx']
  try:
   b.write_json(p,record)
   with self.assertRaises(AssertionError):a.inputs()
  finally:p.write_bytes(original)
 def test_stale_pack_verification(self):
  p=b.TMP/'verified-desktop.json';original=p.read_bytes();record=json.loads(original);record['input_hashes'][a.PACK]='0'*64
  try:
   b.write_json(p,record)
   with self.assertRaisesRegex(AssertionError,'Stale input'):b.manifest()
  finally:p.write_bytes(original)
 def test_extracted_cache_not_trusted(self):
  p=P/'tmp/gesture-pack/head nod yes.fbx';p.parent.mkdir(exist_ok=True);p.write_bytes(b'corrupt cache')
  a.load(a.PACK+'!head nod yes.fbx')
  self.assertEqual(b.sha(p),a.inputs()[a.PACK+'!head nod yes.fbx'])

class Round3Preservation(unittest.TestCase):
 def setUp(self):
  import verify_animations as v
  self.verify=v.verify_prefix
  self.doc,self.binary=b.read_glb(b.STAGE/'renamon-desktop.glb')
  self.base,self.original=b.read_glb(b.TMP/'round3/renamon-desktop.glb')
 def test_old_channels_cannot_change(self):
  self.doc['animations'][0]['channels'][0]['target']['path']='translation'
  with self.assertRaisesRegex(AssertionError,'Round-3 animation structure'):self.verify(self.doc,self.binary,self.base,self.original)
 def test_old_samples_cannot_change(self):
  sampler=self.base['animations'][0]['samplers'][0]
  view=self.base['bufferViews'][self.base['accessors'][sampler['output']]['bufferView']]
  data=bytearray(self.binary);data[view['byteOffset']]^=1
  with self.assertRaisesRegex(AssertionError,'payload changed'):self.verify(self.doc,bytes(data),self.base,self.original)

class DeterministicPNG(unittest.TestCase):
 def test_volatile_metadata_removed_without_pixel_changes(self):
  import struct,zlib
  from deterministic_png import normalize
  def chunk(kind,data):return struct.pack('>I',len(data))+kind+data+struct.pack('>I',zlib.crc32(kind+data))
  signature=b'\x89PNG\r\n\x1a\n'
  pixels=chunk(b'IDAT',b'encoded pixels must remain exact')
  frame=chunk(b'tEXt',b'Frame\x00012');end=chunk(b'IEND',b'')
  first=signature+chunk(b'tEXt',b'Date\x00first')+pixels+chunk(b'tEXt',b'RenderTime\x001.0')+frame+end
  second=signature+chunk(b'tEXt',b'Date\x00second')+pixels+chunk(b'tEXt',b'RenderTime\x002.0')+frame+end
  expected=signature+pixels+frame+end
  self.assertEqual(normalize(first),expected);self.assertEqual(normalize(second),expected)
  self.assertEqual(normalize(expected),expected)
 def test_corrupt_png_rejected_without_writing(self):
  from deterministic_png import normalize_folder
  folder=P/'tmp/png-failure-test';folder.mkdir(exist_ok=True)
  path=folder/'broken.png';original=b'\x89PNG\r\n\x1a\ntruncated';path.write_bytes(original)
  try:
   with self.assertRaises(ValueError):normalize_folder(folder)
   self.assertEqual(path.read_bytes(),original)
  finally:path.unlink();folder.rmdir()

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
