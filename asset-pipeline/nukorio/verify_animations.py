"""Fresh Blender import, exact immutable-payload checks and motion contact sheets."""
import os
from pathlib import Path
P=Path(__file__).resolve().parent
os.environ['TMPDIR']=str(P/'tmp')
import sys,tempfile
sys.dont_write_bytecode=True;tempfile.tempdir=str(P/'tmp');sys.path.insert(0,str(P))
import build_nukorio as b
import animate_nukorio as a
import bpy,json,copy,math,numpy as np
from mathutils import Vector

def verify_prefix(doc, binary, base, original):
 expected=copy.deepcopy(base);actual=copy.deepcopy(doc)
 actual.pop('animations',None);expected.pop('animations',None)
 actual['accessors']=actual['accessors'][:len(base['accessors'])]
 actual['bufferViews']=actual['bufferViews'][:len(base['bufferViews'])]
 actual['buffers']=expected['buffers']
 assert actual==expected,'Immutable glTF structure changed'
 assert binary[:len(original)]==original,'Original mesh/morph/skin/texture/animation payload changed'
 old=base.get('animations',[])
 assert doc.get('animations',[])[:len(old)]==old,'Round-3 animation structure changed'
 for clip in old:
  for sampler in clip['samplers']:
   for key in ('input','output'):
    assert np.array_equal(b.accessor(doc,binary,sampler[key]),b.accessor(base,original,sampler[key])),'Round-3 animation samples changed'

def verify(v,renders=False):
 a.inputs();path=b.STAGE/f'renamon-{v}.glb';doc,bin=b.read_glb(path)
 basepath=b.TMP/'round2'/path.name;base,bb=b.read_glb(basepath)
 assert b.sha(basepath)==json.loads((P/'visual-baseline.json').read_text())['files'][v]
 verify_prefix(doc,bin,base,bb)
 round3path=b.TMP/'round3'/path.name;round3,round3bin=b.read_glb(round3path)
 assert b.sha(round3path)==json.loads((P/'animation-visual-baseline.json').read_text())['files'][v]
 verify_prefix(doc,bin,round3,round3bin)
 assert len(doc['animations'])==24
 allowed={(ch['target']['node'],ch['target']['path']) for ch in round3['animations'][0]['channels']}
 for clip in doc['animations']:
  assert {(ch['target']['node'],ch['target']['path']) for ch in clip['channels']}==allowed,'Unexpected bone or face track'
 meta=json.loads((b.TMP/'animation-metadata.json').read_text())
 assert [x['name'] for x in doc['animations']]==[x['name'] for x in meta['animations']]
 forbidden=set(json.loads((b.TMP/'recipe-desktop.json').read_text())['skeleton']['tail'])
 for chain in json.loads((b.TMP/'recipe-desktop.json').read_text())['skeleton']['ears'].values():forbidden.update(chain)
 for clip,spec in zip(doc['animations'],meta['animations']):
  for ch in clip['channels']:
   assert ch['target']['path'] in ['rotation','translation']
   n=doc['nodes'][ch['target']['node']]['name'];assert n not in forbidden
   assert ch['target']['path']!='translation' or n=='c_root_bend.x'
   s=clip['samplers'][ch['sampler']];t=b.accessor(doc,bin,s['input']).ravel();vals=b.accessor(doc,bin,s['output'])
   assert abs(t[-1]-spec['duration_seconds'])<1e-5
   assert np.isfinite(vals).all()
   if spec['loop']:assert np.max(np.abs(vals[0]-vals[-1]))<1e-6
 b.clear();bpy.ops.import_scene.gltf(filepath=str(path));arm=next(o for o in bpy.context.scene.objects if o.type=='ARMATURE')
 actions={x.name:x for x in bpy.data.actions};print('ACTIONS',list(actions),flush=True)
 assert len(arm.data.bones)==110
 for tr in arm.animation_data.nla_tracks:tr.mute=True
 arm.animation_data.action=None
 for p in arm.pose.bones:p.matrix_basis.identity()
 for o in list(bpy.context.scene.objects):
  if o.type=='MESH' and o.name not in b.NAMES.values():bpy.data.objects.remove(o,do_unlink=True)
 meshes=[o for o in bpy.context.scene.objects if o.type=='MESH']
 assert len(meshes)==15
 for o in meshes:
  if o.data.shape_keys:
   assert [k.name for k in o.data.shape_keys.key_blocks][1:]==b.MORPHS
   for key in o.data.shape_keys.key_blocks:key.value=0
 # GLB imports may choose different display bone axes; rest geometry is invariant.
 bpy.context.view_layer.update()
 ground=min((o.matrix_world@v.co).z for o in meshes for v in o.data.vertices)
 reports={};renderlist=[]
 if renders:
  b.setup_render(360);s=bpy.context.scene;s.render.resolution_x=300;s.render.resolution_y=360
  s.render.engine='BLENDER_EEVEE';s.eevee.taa_render_samples=16 if hasattr(s.eevee,'taa_render_samples') else 16
  bpy.ops.mesh.primitive_plane_add(size=200,location=(0,0,ground-.002));floor=bpy.context.object;floor.name='verification_ground';floor.data.materials.append(b.new_material('ground',(.12,.12,.12,1)))
 for spec in meta['animations']:
  name=spec['name'];action=next(x for x in bpy.data.actions if x.name==name or x.name.startswith(name+'_'))
  arm.animation_data.action=action
  if action.slots:arm.animation_data.action_slot=action.slots[0]
  bpy.context.scene.render.fps=30
  # Importer uses scene FPS (default 24), so derive frames from seconds explicitly.
  rng=action.frame_range;print('ACTION',name,list(rng),flush=True)
  foot=[]
  for i in range(spec['frames']):
   f=float(rng[0]+(rng[1]-rng[0])*i/max(1,spec['frames']-1));bpy.context.scene.frame_set(int(f),subframe=f-int(f))
   foot.append([list((arm.matrix_world@arm.pose.bones['foot.'+side].matrix).translation) for side in ['l','r']])
  arr=np.array(foot);drift=float(np.max(np.linalg.norm(arr-arr[0],axis=2)));assert drift<.002,(name,drift)
  reports[name]={'duration_seconds':spec['duration_seconds'],'frames':spec['frames'],'foot_bone_drift_m':drift}
  if renders:
   for i in range(5):
    f=float(rng[0]+(rng[1]-rng[0])*i/4);bpy.context.scene.frame_set(int(f),subframe=f-int(f))
    for view in ['front','3q']:
     fn=f'animation_{name}_{view}_{i}.png'
     cam=bpy.context.scene.camera;target=Vector((0,.38 if view=='3q' else .12,1.06));direction=Vector((4,-5,.35) if view=='3q' else (0,-5,.1))
     cam.location=target+direction;cam.rotation_euler=(-direction).to_track_quat('-Z','Y').to_euler();cam.data.ortho_scale=3.1
     bpy.context.scene.render.filepath=str(b.STAGE/'renders'/fn);bpy.ops.render.render(write_still=True);renderlist.append(fn)
 result={'sha256':b.sha(path),'inputs':a.inputs(),'round2_sha256':b.sha(basepath),'round3_sha256':b.sha(round3path),'round3_clips_exact':True,'immutable_json_and_binary_exact':True,'fresh_blender_import':True,'clips':reports,'renders':renderlist,'passed':True}
 b.write_json(b.TMP/f'verified-animation-{v}.json',result)

if __name__=='__main__':
 args=sys.argv[sys.argv.index('--')+1:];verify(args[0],'--renders' in args)
