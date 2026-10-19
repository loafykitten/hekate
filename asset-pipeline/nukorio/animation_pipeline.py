"""Animation stage orchestration; invoked by the complete source build."""
import os
from pathlib import Path
P=Path(__file__).resolve().parent;os.environ['TMPDIR']=str(P/'tmp')
import sys,tempfile
sys.dont_write_bytecode=True;tempfile.tempdir=str(P/'tmp');sys.path.insert(0,str(P))
import build_nukorio as b
import animate_nukorio as a
import json,subprocess,shutil
from delivery import recover,promote

def run(script,args,log):
 with (b.TMP/log).open('w') as out:
  subprocess.run([b.bpy.app.binary_path,'-b','--factory-startup','--disable-autoexec','--python-exit-code','1','--python',str(P/script),'--',*args],env=os.environ.copy(),stdout=out,stderr=subprocess.STDOUT,check=True)

def refresh_records():
 hashes=a.inputs()
 for v in ['desktop','phone']:
  record=json.loads((b.TMP/f'verified-{v}.json').read_text());check=json.loads((b.TMP/f'verified-animation-{v}.json').read_text());path=b.STAGE/f'renamon-{v}.glb'
  assert check['sha256']==b.sha(path) and check['inputs']==hashes
  record.update(sha256=b.sha(path),bytes=path.stat().st_size,input_hashes=hashes,round2_sha256=check['round2_sha256'])
  b.write_json(b.TMP/f'verified-{v}.json',record)

def verify_samples(m):
 docs={v:b.read_glb(b.STAGE/f'renamon-{v}.glb') for v in ['desktop','phone']}
 for ca,cb in zip(docs['desktop'][0]['animations'],docs['phone'][0]['animations']):
  assert ca['name']==cb['name'] and len(ca['channels'])==len(cb['channels'])
  for xa,xb in zip(ca['channels'],cb['channels']):
   da,ba=docs['desktop'];db,bb=docs['phone']
   assert da['nodes'][xa['target']['node']]['name']==db['nodes'][xb['target']['node']]['name']
   assert xa['target']['path']==xb['target']['path']
   for key in ['input','output']:
    assert b.np.array_equal(b.accessor(da,ba,ca['samplers'][xa['sampler']][key]),b.accessor(db,bb,cb['samplers'][xb['sampler']][key]))
 for v in ['desktop','phone']:
  added=m['files'][v]['bytes']-(b.TMP/'round2'/f'renamon-{v}.glb').stat().st_size
  assert 0<added<=3_000_000
  m['files'][v]['animation_bytes_added']=added
 m['verification']['animation_samples_identical_between_variants']=True
 return m


def finish(build=True,publish=True):
 recover(b.OUT,b.TMP);a.inputs()
 if build:
  ref=b.TMP/'round2';ref.mkdir(exist_ok=True)
  baseline=json.loads((P/'visual-baseline.json').read_text())
  for v in ['desktop','phone']:
   path=b.STAGE/f'renamon-{v}.glb';assert b.sha(path)==baseline['files'][v]
   shutil.copy2(path,ref/path.name)
  shutil.copy2(b.STAGE/'manifest.json',ref/'manifest.json')
  run('animate_nukorio.py',[],'animate.log')
 for v in ['desktop','phone']:run('verify_animations.py',[v]+(['--renders'] if v=='desktop' else []),f'verify-animation-{v}.log')
 run('contact_sheets.py',[],'contact-sheets.log')
 refresh_records();m=b.manifest()
 verify_samples(m)
 with (b.TMP/'threejs-animation.log').open('w') as log:subprocess.run([shutil.which('node'),str(P/'verify_threejs.mjs')],env=os.environ.copy(),stdout=log,stderr=subprocess.STDOUT,check=True)
 report=json.loads((b.TMP/'threejs_verification.json').read_text())
 m['verification']['threejs']=report
 m['skeleton']['threejs_runtime_names']=report['variants']['desktop']['bone_runtime_names']
 m['skeleton']['threejs_binding_note']='GLTFLoader sanitizes punctuation (head.x -> headx). Imported animation tracks already use runtime names; use this map for procedural layers.'
 m['verification']['animation_fresh_import']={v:json.loads((b.TMP/f'verified-animation-{v}.json').read_text()) for v in ['desktop','phone']}
 m['verification']['round2_payload_exact']=True
 m['verification']['animation_contact_sheets']=[f'animation_{x[0]}_contact.png' for x in a.CLIPS]
 m['animation_notes']={'retarget':'T/A calibration in world space, own-rest relative rotations; leg-length-scaled vertical hip motion; no horizontal root motion. Source leg rotations seed a three-link IK stance; feet and toes hold their rest-plane contacts. Footwork is deliberately adapted for a stationary companion.', 'hands':'Thumb, Index, Pinky map to the matching three target digits. Wrist reach is corrected around torso and moving head clearance volumes.', 'head_layer':'Reduce procedural head motion on head_motion=strong clips; idle includes substantial looking around. idle_2 is the quiet idle.', 'transitions':'One-shots use 0.4-second smooth bookends to idle frame zero. Loop tails blend over 0.5 seconds. standing_pose is static.', 'key_reduction':'Maximum per-track interpolation error 0.05 degrees rotation / 0.05 mm translation; final CPU-skinned foot contacts verified at 60 Hz.', 'excluded_tracks':'No tail, ear, morph, face-bone or mesh tracks. Tail and ears still inherit torso/head motion.'}
 b.write_json(b.STAGE/'manifest.json',m)
 if not publish:return
 baseline=json.loads((P/'animation-visual-baseline.json').read_text())
 assert baseline['inputs']==a.inputs()
 for v in ['desktop','phone']:assert baseline['files'][v]==m['files'][v]['sha256'],('Animation visual calibration drift',v)
 m['verification']['visual_review']={'baseline':'asset-pipeline/nukorio/animation-visual-baseline.json','reviewed_glb_hashes_match':True,'notes':baseline['notes'],'reviewed_renders':baseline['reviewed_renders']}
 b.write_json(b.STAGE/'manifest.json',m)
 def validate(folder):
  current=json.loads((folder/'manifest.json').read_text())
  for v,f in current['files'].items():assert b.sha(folder/f['name'])==f['sha256']
  assert current['verification']['threejs']['passed']
 promote(b.STAGE,b.OUT,b.TMP,validate)
 print('ANIMATION DELIVERY VERIFIED',b.OUT,flush=True)

if __name__=='__main__':
 args=sys.argv[sys.argv.index('--')+1:] if '--' in sys.argv else []
 finish(build='--existing' not in args,publish='--review' not in args)
