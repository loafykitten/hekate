"""Offline source-rig evaluation, GLB export, fresh-process verification and staging."""
import os
from pathlib import Path
PIPE = Path(__file__).resolve().parent
ROOT = PIPE.parent.parent
TMP = PIPE / 'tmp'
TMP.mkdir(parents=True, exist_ok=True)
os.environ['TMPDIR'] = str(TMP)
import tempfile, sys
sys.path.insert(0,str(PIPE))
sys.dont_write_bytecode = True
os.environ['PYTHONDONTWRITEBYTECODE'] = '1'
tempfile.tempdir = str(TMP)
import bpy
bpy.context.preferences.filepaths.temporary_directory = str(TMP)
import argparse, hashlib, json, math, shutil, struct, subprocess, sys
import numpy as np
from mathutils import Vector, Matrix, Quaternion

SRC = ROOT / 'Renamon-Nukorio/Renamon_V2.blend'
SOURCE_SHA = '2a0fccedb5994ce10479c44b1b510687931b27342dfab3af88820de890d20d94'
STAGE = TMP / 'staging'
OUT = ROOT / 'assets/renamon-nukorio'
NAMES = {
 'Renamon_Body_Final':'body','Renamon_Body_Hands':'hands','Renamon_Head':'head',
 'Renamon_Ear':'ears','Renamon_Fur':'fur','Renamon_Shoulder_Fur':'shoulder_fur',
 'Renamon_Sleeve':'sleeves','Renamon_Finger_Claws':'claws','Renamon_Finger_Ring':'finger_rings',
 'Renamon_BlackEye':'eye_black','Renamon_Blue_Eye':'eye_blue',
 'Renamon_Blue_Eye_Line.L':'eye_blue_line','Renamon_Eye_Shine':'eye_shine',
 'Renamon_Iris.L':'eye_iris','Renamon_Eye_Line':'eye_line'}
MORPHS = ['jawOpen','viseme_aa','viseme_E','viseme_I','viseme_O','viseme_U','viseme_PP',
          'mouthSmile','mouthFrown','eyeBlink_L','eyeBlink_R','eyeSquint','browUp','browDown']

def sha(p): return hashlib.sha256(p.read_bytes()).hexdigest()
def write_json(p,x): p.write_text(json.dumps(x,indent=2,sort_keys=True)+'\n')
def clear():
 bpy.ops.wm.read_factory_settings(use_empty=True)
 bpy.context.preferences.filepaths.temporary_directory=str(TMP)
def open_source():
 assert sha(SRC)==SOURCE_SHA,'Source SHA-256 changed; recalibration required'
 bpy.ops.wm.open_mainfile(filepath=str(SRC),use_scripts=False)
 bpy.context.preferences.filepaths.temporary_directory=str(TMP)
 a=bpy.data.objects['Renamon_Rig']
 assert len(a.data.bones)==878 and sum(b.use_deform for b in a.data.bones)==330
 assert bpy.context.scene.render.fps==24
 drivers=[];seen=set()
 for collection in [bpy.data.objects,bpy.data.armatures,bpy.data.shape_keys,bpy.data.materials,bpy.data.node_groups,bpy.data.scenes,bpy.data.meshes]:
  for data in collection:
   for block in [data,getattr(data,'node_tree',None)]:
    if block and block.as_pointer() not in seen:
     seen.add(block.as_pointer())
     if block.animation_data:drivers.extend(block.animation_data.drivers)
 assert len(drivers)==562,('Source driver count changed',len(drivers))
 assert all(f.driver.type!='SCRIPTED' or f.driver.is_simple_expression for f in drivers),'Non-simple driver found'
 if a.animation_data:
  a.animation_data.action=None
  for t in a.animation_data.nla_tracks:t.mute=True
 for p in a.pose.bones:p.matrix_basis.identity()
 for c in bpy.data.collections:c.hide_viewport=False
 def unexclude(lc):
  lc.exclude=False;lc.hide_viewport=False
  for c in lc.children:unexclude(c)
 unexclude(bpy.context.view_layer.layer_collection)
 for o in bpy.context.scene.objects:
  if o.name in NAMES or o.name.startswith('HLP_') or o==a or o.name=='Renamon_Head_Proxy':o.hide_set(False);o.hide_viewport=False
  o.hide_render=o.name not in NAMES
 bpy.context.view_layer.update()
 return a

def configure(levels=1):
 bpy.context.scene.render.use_simplify=False
 for src in NAMES:
  for m in bpy.data.objects[src].modifiers:
   if m.type=='NODES' and m.node_group and 'Outline' in m.node_group.name:m.show_viewport=False;m.show_render=False
   else:
    m.show_viewport=m.show_render
    if m.type=='SUBSURF':m.levels=levels;m.render_levels=levels
 # The artist saved a bound 48-vertex smooth after a 24-vertex mask. Blender
 # skips this invalid modifier itself; explicitly disable only that no-op.
 for m in bpy.data.objects['HLP_Renamon_Ribbon_Lip'].modifiers:
  if m.type=='CORRECTIVE_SMOOTH':m.show_viewport=False;m.show_render=False
 bpy.context.view_layer.update()

def move_world(a,name,delta):
 p=a.pose.bones[name];p.location=p.bone.matrix_local.to_3x3().inverted()@Vector(delta)

def pose(a,name):
 for p in a.pose.bones:p.matrix_basis.identity()
 jaw={'jawOpen':.30,'viseme_aa':.27,'viseme_E':.15,'viseme_I':.065,'viseme_O':.20,'viseme_U':.055}.get(name,0)
 p=a.pose.bones['DEF_Jaw'];p.rotation_mode='XYZ';p.rotation_euler.x=jaw
 corners={'viseme_aa':(.003,0),'viseme_E':(.016,.005),'viseme_I':(.020,.002),
 'viseme_O':(-.018,0),'viseme_U':(-.024,0),'viseme_PP':(-.004,-.001),
 'mouthSmile':(.012,.018),'mouthFrown':(-.003,-.015)}
 if name in corners:
  x,y=corners[name]
  for s,sign in [('L',1),('R',-1)]:a.pose.bones['CTRL_Lip_Main_Corn.'+s].location=(sign*x,y,0)
 if name=='viseme_PP':
  move_world(a,'CTRL_Lip_Main_Top',(0,0,-.0012))
  move_world(a,'CTRL_Lip_Main_Bottom',(0,-.0003,.0010))
 if name in ['eyeBlink_L','eyeBlink_R','eyeSquint']:
  sides=['L','R'] if name=='eyeSquint' else [name[-1]]
  for s in sides:
   # Pose the source ribbon's three top and three bottom handles, preserving
   # the existing spherical tracking constraints on the 18 lid deform bones.
   for part in ['In','Mid','Out']:
    top=a.data.bones[f'DEF_Eyelid_{part}_Top.{s}'].head_local
    bot=a.data.bones[f'DEF_Eyelid_{part}_Bottom.{s}'].head_local
    d=bot-top
    t,b=(.16,.68) if name=='eyeSquint' else (.98,.38)
    move_world(a,f'DEF_Eyelid_{part}_Top.{s}',d*t)
    move_world(a,f'DEF_Eyelid_{part}_Bottom.{s}',-d*b)
 if name in ['browUp','browDown']:
  for s in ['L','R']:
   move_world(a,'CTRL_EyeWrinkle.'+s,(0,0,.008 if name=='browUp' else -.006))
 a.update_tag();bpy.context.view_layer.update()
 # Update again so constraints depending on helper mesh evaluation settle.
 bpy.context.scene.frame_set(1);bpy.context.view_layer.update()

def evaluated(o):
 dg=bpy.context.evaluated_depsgraph_get()
 return bpy.data.meshes.new_from_object(o.evaluated_get(dg),preserve_all_data_layers=True,depsgraph=dg)
def coords(m):
 x=np.empty(len(m.vertices)*3,dtype=np.float32);m.vertices.foreach_get('co',x);return x.reshape(-1,3)

def setup_render(res=640):
 s=bpy.context.scene;s.render.engine='BLENDER_EEVEE'
 s.render.resolution_x=res;s.render.resolution_y=res;s.render.resolution_percentage=100
 s.render.image_settings.file_format='PNG';s.render.film_transparent=False
 s.view_settings.view_transform='Standard';s.view_settings.look='None';s.view_settings.exposure=0;s.view_settings.gamma=1
 w=bpy.data.worlds.new('Verification studio');s.world=w
 w.node_tree.nodes['Background'].inputs['Color'].default_value=(.055,.055,.055,1)
 w.node_tree.nodes['Background'].inputs['Strength'].default_value=.8
 for loc,power,size in [((2,-4,4),240,4),((-3,-2,2.4),140,4),((1,3,3),180,3),((0,-4,1.4),160,5)]:
  bpy.ops.object.light_add(type='AREA',location=loc);o=bpy.context.object;o.data.energy=power;o.data.shape='DISK';o.data.size=size
  o.rotation_euler=(Vector((0,0,1.4))-o.location).to_track_quat('-Z','Y').to_euler()
 bpy.ops.object.camera_add();s.camera=bpy.context.object;s.camera.data.type='ORTHO'
 return s

def render(path,view='3q',full=False):
 s=bpy.context.scene;cam=s.camera
 target=Vector((0,-.04,1.70) if not full else (0,.12,1.06))
 direction=Vector((4,-5,.35) if view=='3q' else (0,-5,.1))
 cam.location=target+direction;cam.rotation_euler=(-direction).to_track_quat('-Z','Y').to_euler()
 cam.data.ortho_scale=.90 if not full else 2.5
 s.render.filepath=str(path);bpy.ops.render.render(write_still=True)


def outline_recipe():
 result={}
 def hexcolor(rgb):
  return '#'+''.join(f'{round(255*(12.92*c if c<=.0031308 else 1.055*c**(1/2.4)-.055)):02x}' for c in rgb)
 for src,name in NAMES.items():
  entries=[]
  for m in bpy.data.objects[src].modifiers:
   if m.type!='NODES' or not m.node_group or 'Outline' not in m.node_group.name:continue
   def socket(n):return getattr(m.properties.inputs,n).value
   mat=socket('Socket_5');rgb=list(mat.node_tree.nodes['Emission'].inputs['Color'].default_value)[:3]
   entries.append(dict(material=mat.name,color_linear=rgb,color_hex=hexcolor(rgb),
     thickness_m=socket('Socket_2')/1000,source_thickness=socket('Socket_2'),
     noise_scale=socket('Socket_3'),noise_strength=socket('Socket_4'),
     enabled=bool(m.show_render and socket('Socket_6'))))
  result[name]=dict(material='body_pbr' if not name.startswith('eye_') else name,entries=entries)
 return result

def skeleton_recipe(a):
 # Only weighted body/helper deform bones, plus the main humanoid and tail/ear
 # chains. Facial descendants are merged into head.x at the weight step.
 body=set()
 for src in NAMES:
  o=bpy.data.objects[src]
  for v in o.data.vertices:
   for g in v.groups:
    n=o.vertex_groups[g.group].name
    b=a.data.bones.get(n)
    if g.weight>1e-7 and b and b.use_deform and (n=='head.x' or n.startswith('DEF_Ear') or a.data.bones['head.x'] not in b.parent_recursive):body.add(n)
 # Mirror modifiers populate otherwise empty opposite-side vertex groups.
 for n in list(body):
  opposite=n[:-1]+{'l':'r','r':'l','L':'R','R':'L'}.get(n[-1],n[-1])
  if opposite in a.data.bones and a.data.bones[opposite].use_deform:body.add(opposite)
 parent={'c_root_bend.x':None,'c_waist_bend.x':'c_root_bend.x','c_spine_01_bend.x':'c_root_bend.x',
         'c_spine_02_bend.x':'c_spine_01_bend.x','c_spine_03_bend.x':'c_spine_02_bend.x',
         'neck.x':'c_spine_03_bend.x','head.x':'neck.x'}
 human={'hips':'c_root_bend.x','spine':'c_spine_01_bend.x','chest':'c_spine_02_bend.x','upperChest':'c_spine_03_bend.x','neck':'neck.x','head':'head.x'}
 for s,side in [('l','left'),('r','right')]:
  S=s.upper()
  parent.update({f'shoulder.{s}':'c_spine_03_bend.x',f'arm_stretch.{s}':f'shoulder.{s}',f'forearm_stretch.{s}':f'arm_stretch.{s}',f'hand.{s}':f'forearm_stretch.{s}',
   f'thigh_b_str.{s}':'c_root_bend.x',f'thigh_stretch.{s}':f'thigh_b_str.{s}',f'leg_stretch.{s}':f'thigh_stretch.{s}',f'foot.{s}':f'leg_stretch.{s}',f'toes_01.{s}':f'foot.{s}'})
  for standard,n in [('Shoulder','shoulder'),('UpperArm','arm_stretch'),('LowerArm','forearm_stretch'),('Hand','hand'),('UpperLeg','thigh_b_str'),('LowerLeg','thigh_stretch'),('Foot','foot'),('Toes','toes_01')]:human[side+standard]=n+'.'+s
  for digit in ['thumb','index','pinky']:
   chain=[f'{digit}1.{s}',f'c_{digit}2.{s}',f'c_{digit}3.{s}']
   for i,n in enumerate(chain):
    parent[n]=f'hand.{s}' if i==0 else chain[i-1]
    human[side+digit.title()+['Proximal','Intermediate','Distal'][i]]=n
  for i in range(1,4):parent[f'DEF_Ear{i}.{S}']='head.x' if i==1 else f'DEF_Ear{i-1}.{S}'
 tail=[f'spline_twist_{i:02}.x' for i in range(1,8)]+['spline_def_tail.x']
 for i,n in enumerate(tail):parent[n]='c_root_bend.x' if i==0 else tail[i-1]
 body.update(parent)
 for n in sorted(body):
  if n in parent:continue
  b=a.data.bones[n];p=b.parent
  while p and p.name not in body:p=p.parent
  parent[n]=p.name if p else 'c_root_bend.x'
  # ARP constraint-driven helpers do not have usable deform ancestors.
  side=n[-1].lower()
  if side in ['l','r']:
   if 'Shoulder' in n and not (p and 'Shoulder' in p.name):parent[n]='shoulder.'+side
   elif 'Hip' in n or 'thigh_out' in n:parent[n]='thigh_b_str.'+side
   elif 'knee_out' in n:parent[n]='thigh_stretch.'+side
   elif 'elbow_out' in n:parent[n]='forearm_stretch.'+side
   elif n.startswith('TWK_'):
    digit=n.split('_')[1].lower();num=next((c for c in digit if c.isdigit()),'1');digit=''.join(c for c in digit if not c.isdigit())
    # Edge helpers carry the same digit segment as the matching hand bone.
    digit=next((d for d in ['thumb','index','pinky'] if d in n.lower()),'index')
    num=next((c for c in n if c.isdigit()),'1')
    parent[n]=(f'{digit}1.{side}' if num=='1' else f'c_{digit}{num}.{side}')
 bones=[]
 for n in sorted(body):
  b=a.data.bones[n]
  bones.append(dict(name=n,parent=parent[n],matrix=[list(r) for r in b.matrix_local],length=b.length))
 axes={k:list(a.data.bones[n].matrix_local.to_3x3().inverted()@Vector((1,0,0))) for k,n in human.items()}
 return dict(bones=bones,humanoid=human,tail=tail,ears={s:[f'DEF_Ear{i}.{s}' for i in range(1,4)] for s in ['L','R']},head_bone='head.x',local_pitch_axes=axes,
  pitch_axis_convention='Positive rotation about this local vector pitches the forward (-Y Blender/+Z glTF) direction downward in the rest pose.',
  missing_humanoid=['leftMiddle','leftRing','rightMiddle','rightRing'],
  retarget_notes='Three digits per hand: thumb, index, pinky. Digitigrade legs: thigh_b_str -> thigh_stretch -> leg_stretch -> foot. leg_stretch is an extra hock segment; distribute lower-leg rotation deliberately.')

def create_armature(recipe):
 data=bpy.data.armatures.new('renamon_skeleton');arm=bpy.data.objects.new('renamon_skeleton',data);bpy.context.collection.objects.link(arm)
 bpy.context.view_layer.objects.active=arm;arm.select_set(True);bpy.ops.object.mode_set(mode='EDIT')
 for entry in recipe['bones']:
  b=data.edit_bones.new(entry['name']);b.length=entry['length'];b.matrix=Matrix(entry['matrix']);b.use_deform=True
 for entry in recipe['bones']:
  if entry['parent']:data.edit_bones[entry['name']].parent=data.edit_bones[entry['parent']]
 bpy.ops.object.mode_set(mode='OBJECT')
 for entry in recipe['bones']:
  actual=data.bones[entry['name']].matrix_local
  assert max(abs(actual[i][j]-entry['matrix'][i][j]) for i in range(4) for j in range(4))<1e-5,entry['name']
 return arm

def reskin(o,source,a,recipe):
 kept={b['name'] for b in recipe['bones']};mapping={}
 for g in source.vertex_groups:
  b=a.data.bones.get(g.name)
  while b and b.name not in kept:b=b.parent
  mapping[g.index]=b.name if b else 'head.x' if NAMES[source.name].startswith('eye_') or source.name=='Renamon_Head' else 'c_root_bend.x'
 weights=[]
 for v in o.data.vertices:
  ws={}
  for g in v.groups:
   # Only actual deform groups contribute; ribbon/debug vertex groups do not.
   name=source.vertex_groups[g.group].name
   bone=a.data.bones.get(name)
   if bone and bone.use_deform:
    n=mapping[g.group];ws[n]=ws.get(n,0)+g.weight
  ws=sorted(ws.items(),key=lambda x:(-x[1],x[0]))[:4]
  total=sum(w for _,w in ws)
  if total<1e-8:ws=[('head.x' if source.name=='Renamon_Head' or NAMES[source.name].startswith('eye_') else 'c_root_bend.x',1)];total=1
  weights.append([(n,w/total) for n,w in ws if w/total>1e-7])
 # Clear source indices with matching group definitions before replacing them.
 for g in source.vertex_groups:o.vertex_groups.new(name=g.name)
 o.vertex_groups.clear()
 for n in sorted({n for ws in weights for n,_ in ws}):o.vertex_groups.new(name=n)
 for i,ws in enumerate(weights):
  total=sum(w for _,w in ws)
  for n,w in ws:o.vertex_groups[n].add([i],w/total,'REPLACE')

def decimate(o,ratio):
 if ratio>=1:return
 bpy.context.view_layer.objects.active=o;o.select_set(True)
 m=o.modifiers.new('budget reduction','DECIMATE');m.ratio=ratio;m.use_collapse_triangulate=True
 bpy.ops.object.modifier_apply(modifier=m.name)

def transfer_map(source,target):
 from mathutils.bvhtree import BVHTree
 source.calc_loop_triangles();tri=np.array([t.vertices[:] for t in source.loop_triangles],dtype=np.int32);xyz=coords(source)
 tree=BVHTree.FromPolygons(xyz.tolist(),tri.tolist(),all_triangles=True)
 indices=[];bary=[];maxdist=0
 for v in target.vertices:
  p,_,i,d=tree.find_nearest(v.co);ids=tri[i];a,b,c=xyz[ids];v0=b-a;v1=c-a;v2=np.array(p)-a
  d00=v0@v0;d01=v0@v1;d11=v1@v1;d20=v2@v0;d21=v2@v1;den=d00*d11-d01*d01
  if abs(den)<1e-20:w=np.array([1.,0,0])
  else:
   y=(d11*d20-d01*d21)/den;z=(d00*d21-d01*d20)/den;w=np.array([1-y-z,y,z])
  indices.append(ids);bary.append(w);maxdist=max(maxdist,d)
 return np.array(indices),np.array(bary,dtype=np.float32),maxdist

def new_material(name,color=(1,1,1,1),image=None,unlit=False):
 m=bpy.data.materials.new(name);nodes=m.node_tree.nodes;nodes.clear();links=m.node_tree.links;out=nodes.new('ShaderNodeOutputMaterial')
 if unlit:
  if image:
   shader=nodes.new('ShaderNodeTexImage');shader.image=image
  else:
   shader=nodes.new('ShaderNodeRGB');shader.outputs[0].default_value=color
  # A color socket into Surface is Blender's supported KHR_materials_unlit pattern.
  links.new(shader.outputs[0],out.inputs['Surface'])
 else:
  shader=nodes.new('ShaderNodeBsdfPrincipled');shader.inputs['Base Color'].default_value=color
  shader.inputs['Roughness'].default_value=1;shader.inputs['Metallic'].default_value=0
  if image:
   t=nodes.new('ShaderNodeTexImage');t.image=image;links.new(t.outputs['Color'],shader.inputs['Base Color'])
  links.new(shader.outputs[0],out.inputs['Surface'])
 m.diffuse_color=color;m.use_backface_culling=True
 return m

def make_materials(size):
 image=bpy.data.images['Renamon_V7_BaseColor.png'].copy();image.name=f'body_basecolor_{size}';image.scale(size,size)
 image.file_format='PNG';image.filepath_raw=str(TMP/f'body_basecolor_{size}.png');image.save()
 # A copied packed image retains the original packed PNG even after scaling.
 # Reload the saved downsample so the exporter embeds the actual new bytes.
 bpy.data.images.remove(image)
 image=bpy.data.images.load(str(TMP/f'body_basecolor_{size}.png'),check_existing=False);image.name=f'body_basecolor_{size}';image.pack()
 mats={'Renamon_Texture':new_material('body_pbr',image=image)}
 colors={'Renamon_BlackEye':'eye_black','Renamon_BlueEyeLine':'eye_blue_line','Renamon_Eye_Shine':'eye_shine','Material.016':'eye_iris'}
 for src,name in colors.items():
  col=bpy.data.materials[src].node_tree.nodes['Emission'].inputs['Color'].default_value[:]
  mats[src]=new_material(name,col,unlit=True)
 mats['Renamon_EyeLine']=new_material('eye_line',bpy.data.materials['Renamon_EyeLine'].node_tree.nodes['Principled BSDF'].inputs['Base Color'].default_value[:])
 ramp=bpy.data.materials['Renamon_BlueEye'].node_tree.nodes['Color Ramp'].color_ramp
 # Bake the original generated-Z ramp once, attach it with stable UVs.
 pixels=[]
 for y in range(128):
  col=ramp.evaluate(y/127)
  pixels.extend(list(col)*8)
 im=bpy.data.images.new('eye_blue_gradient',width=8,height=128,float_buffer=True);im.pixels[:]=pixels
 im.file_format='PNG';im.filepath_raw=str(TMP/'eye_blue_gradient.png');im.save();im.pack()
 mats['Renamon_BlueEye']=new_material('eye_blue',image=im,unlit=True)
 return mats

def build_variant(variant,recipe=None):
 a=open_source();outlines=outline_recipe();skel=skeleton_recipe(a)
 configure(0)
 for src,name in NAMES.items():
  for m in bpy.data.objects[src].modifiers:
   if m.type=='SUBSURF':
    level=(2 if variant=='desktop' else 1) if name=='head' else 1 if name in ['fur','eye_line'] or (name=='body' and variant=='desktop') else 0
    m.levels=level;m.render_levels=level
 pose(a,'neutral')
 bases={src:evaluated(bpy.data.objects[src]) for src in NAMES}
 basecoords={src:coords(m) for src,m in bases.items()}
 topology={}
 for src,m in bases.items():
  indices=np.empty(len(m.loops),dtype=np.int32);m.loops.foreach_get('vertex_index',indices);topology[src]=indices
 captures={src:{} for src in NAMES};deltas={}
 for morph in MORPHS:
  pose(a,morph);deltas[morph]={}
  for src in NAMES:
   m=evaluated(bpy.data.objects[src]);x=coords(m)
   assert x.shape==basecoords[src].shape,(src,morph,'topology changed')
   assert len(m.loops)==len(bases[src].loops)
   indices=np.empty(len(m.loops),dtype=np.int32);m.loops.foreach_get('vertex_index',indices)
   assert np.array_equal(indices,topology[src]),(src,morph,'topology order changed')
   delta=x-basecoords[src];distance=float(np.max(np.linalg.norm(delta,axis=1)))
   deltas[morph][NAMES[src]]=distance
   captures[src][morph]=delta
   bpy.data.meshes.remove(m)
  print('CAPTURE',variant,morph,{n:round(d,6) for n,d in deltas[morph].items() if d>1e-6},flush=True)
 pose(a,'neutral');mats=make_materials(2048 if variant=='desktop' else 1024)
 # Put the extracted data in a new scene so the source control rig cannot export.
 source_scene=bpy.context.scene;scene=bpy.data.scenes.new('Export '+variant);bpy.context.window.scene=scene
 scene.unit_settings.system='METRIC';scene.unit_settings.scale_length=1;scene.render.fps=24
 arm=create_armature(skel);built=[];stats=[]
 for src,name in NAMES.items():
  source=bpy.data.objects[src];mesh=bases[src].copy();mesh.name=name+'_geometry';o=bpy.data.objects.new(name,mesh);scene.collection.objects.link(o)
  reskin(o,source,a,skel)
  ratio={'body':.23,'fur':.55,'ears':.60,'claws':.70}.get(name,1) if variant=='desktop' else (.12 if name=='fur' else .46 if name not in ['head','eye_line','eye_black'] and not name.startswith('eye_') else .5 if name in ['eye_blue','eye_blue_line','eye_shine','eye_iris'] else 1)
  decimate(o,ratio);reduced=coords(o.data)
  index=bary=None;dist=0
  if ratio<1:index,bary,dist=transfer_map(bases[src],o.data)
  moved=any(deltas[k][name]>1e-6 for k in MORPHS)
  if moved:
   o.shape_key_add(name='Basis')
   for morph in MORPHS:
    delta=captures[src][morph]
    if ratio<1:delta=np.sum(delta[index]*bary[:,:,None],axis=1)
    key=o.shape_key_add(name=morph);key.data.foreach_set('co',(reduced+delta).ravel());key.value=0
  original_mat=source.data.materials[0].name;o.data.materials.clear();o.data.materials.append(mats[original_mat])
  for p in o.data.polygons:p.material_index=0
  if name=='eye_blue':
   # Generated Z in the source shader is normalized in the unposed source mesh.
   lo=min(v.co.z for v in source.data.vertices);hi=max(v.co.z for v in source.data.vertices)
   for uv in list(o.data.uv_layers):o.data.uv_layers.remove(uv)
   uv=o.data.uv_layers.new(name='EyeGradient')
   for loop in o.data.loops:uv.data[loop.index].uv=(.5,(o.data.vertices[loop.vertex_index].co.z-lo)/(hi-lo))
  mod=o.modifiers.new('skin','ARMATURE');mod.object=arm;mod.use_deform_preserve_volume=False;o.parent=arm
  built.append(o);stats.append(dict(name=name,source=src,triangles=sum(len(p.vertices)-2 for p in o.data.polygons),morphs=MORPHS if moved else [],decimation_ratio=ratio,transfer_surface_max_distance_m=dist))
 # Reduction interpolates weights; limit/normalize once more after collapse.
 for o in built:
  for v in o.data.vertices:
   weights=sorted([(g.group,g.weight) for g in v.groups if g.weight>1e-8],key=lambda x:(-x[1],x[0]))[:4];total=sum(w for _,w in weights)
   assert total>0
   for g in o.vertex_groups:g.remove([v.index])
   for i,w in weights:o.vertex_groups[i].add([v.index],w/total,'REPLACE')
 total=sum(s['triangles'] for s in stats);assert total<=(80000 if variant=='desktop' else 30000),(variant,total)
 assert all(max(deltas[m].values())>1e-6 for m in MORPHS),deltas
 reference={}
 for o in built:
  reference[o.name+'__Basis']=coords(o.data)
  if o.data.shape_keys:
   for k in o.data.shape_keys.key_blocks:
    x=np.empty(len(k.data)*3,dtype=np.float32);k.data.foreach_get('co',x);reference[o.name+'__'+k.name]=x.reshape(-1,3)
 np.savez_compressed(TMP/f'reference-{variant}.npz',**reference)
 for o in bpy.context.view_layer.objects:o.select_set(False)
 arm.select_set(True)
 for o in built:o.select_set(True)
 bpy.context.view_layer.objects.active=arm
 path=STAGE/f'renamon-{variant}.glb'
 bpy.ops.export_scene.gltf(filepath=str(path),export_format='GLB',use_selection=True,use_active_scene=True,export_yup=True,export_texcoords=True,export_normals=True,
  export_materials='EXPORT',export_image_format='AUTO',export_animations=False,export_skins=True,export_def_bones=False,export_all_influences=False,
  export_morph=True,export_morph_normal=True,export_morph_tangent=False,export_extras=False,export_cameras=False,export_lights=False,
  export_apply=False,export_rest_position_armature=True)
 calibrate_round2_uv(path,variant)
 result=dict(skeleton=skel,outline=outlines,meshes=stats,source_sha256=SOURCE_SHA,capture_max_delta_m=deltas)
 write_json(TMP/f'recipe-{variant}.json',result)
 print('EXPORTED',variant,total,path.stat().st_size,flush=True)
 return result


def calibrate_round2_uv(path,variant):
 # Decimate can perturb six phone-fur UVs between processes. Pin only that
 # calibrated accessor, with a strict bound, then demand the entire old hash.
 if variant!='phone':return
 import base64
 calibration=json.loads((PIPE/'round2-uv-calibration.json').read_text())
 assert calibration['source_sha256']==SOURCE_SHA
 doc,binary=read_glb(path);idx=calibration['accessor']
 assert doc['accessors'][idx]==calibration['accessor_spec']
 view=doc['bufferViews'][doc['accessors'][idx]['bufferView']]
 assert view==calibration['buffer_view_spec']
 assert any(m['name']=='fur_geometry' and any(p['attributes'].get('TEXCOORD_0')==idx for p in m['primitives']) for m in doc['meshes'])
 payload=base64.b64decode(calibration['payload_base64'])
 assert hashlib.sha256(payload).hexdigest()==calibration['payload_sha256']
 start=view['byteOffset'];old=binary[start:start+view['byteLength']]
 error=float(np.max(np.abs(np.frombuffer(old,dtype='<f4')-np.frombuffer(payload,dtype='<f4'))))
 assert error<=calibration['max_uv_deviation'],('Phone UV calibration drift',error)
 raw=bytearray(path.read_bytes());binstart=28+struct.unpack_from('<I',raw,12)[0]
 raw[binstart+start:binstart+start+len(payload)]=payload
 assert hashlib.sha256(raw).hexdigest()==calibration['round2_sha256'],'Non-UV Round 2 drift; refusing calibration'
 path.write_bytes(raw)
 print('PHONE UV CALIBRATION max difference',error,flush=True)


def read_glb(path):
 raw=path.read_bytes();assert raw[:4]==b'glTF' and struct.unpack_from('<I',raw,4)[0]==2
 size=struct.unpack_from('<I',raw,12)[0];doc=json.loads(raw[20:20+size]);offset=20+size
 assert raw[offset+4:offset+8]==b'BIN\0';binary=raw[offset+8:]
 return doc,binary

def accessor(doc,binary,i):
 a=doc['accessors'][i];v=doc['bufferViews'][a['bufferView']]
 dtype={5121:'u1',5123:'<u2',5125:'<u4',5126:'<f4'}[a['componentType']]
 width={'SCALAR':1,'VEC2':2,'VEC3':3,'VEC4':4,'MAT4':16}[a['type']]
 dt=np.dtype(dtype);offset=v.get('byteOffset',0)+a.get('byteOffset',0)
 return np.ndarray((a['count'],width),dtype=dt,buffer=binary,offset=offset,strides=(v.get('byteStride',width*dt.itemsize),dt.itemsize)).copy()

COMBOS={'jaw_smile':{'jawOpen':.6,'mouthSmile':.5},'O_left_blink':{'viseme_O':1,'eyeBlink_L':1},'jaw_both_blinks':{'jawOpen':1,'eyeBlink_L':1,'eyeBlink_R':1},'jaw_smile_max':{'jawOpen':1,'mouthSmile':1},'smile_squint':{'mouthSmile':.7,'eyeSquint':1}}

def set_morphs(meshes,weights):
 for o in meshes:
  if o.data.shape_keys:
   for key in o.data.shape_keys.key_blocks:key.value=weights.get(key.name,0)
 bpy.context.view_layer.update()

def verify_variant(variant,renders=True):
 from mathutils.kdtree import KDTree
 clear();path=STAGE/f'renamon-{variant}.glb';doc,binary=read_glb(path)
 recipe=json.loads((TMP/f'recipe-{variant}.json').read_text());reference=np.load(TMP/f'reference-{variant}.npz')
 bpy.ops.import_scene.gltf(filepath=str(path));scene=bpy.context.scene
 arm=next(o for o in scene.objects if o.type=='ARMATURE')
 assert {n['name'] for n in doc['nodes'] if 'mesh' in n}==set(NAMES.values())
 # The importer creates an Icosphere bone-display widget, absent from the GLB.
 for o in list(scene.objects):
  if o.type=='MESH' and o.name not in NAMES.values():bpy.data.objects.remove(o,do_unlink=True)
 meshes=sorted([o for o in scene.objects if o.type=='MESH'],key=lambda o:o.name)
 expected={b['name']:b for b in recipe['skeleton']['bones']}
 assert set(arm.data.bones.keys())==set(expected)
 for b in arm.data.bones:
  assert (b.parent.name if b.parent else None)==expected[b.name]['parent'],b.name
 assert not doc.get('animations')
 assert {o.name for o in meshes}==set(NAMES.values()),[o.name for o in meshes]
 assert len(doc['skins'])==1
 skinjoints=[doc['nodes'][i]['name'] for i in doc['skins'][0]['joints']];assert set(skinjoints)==set(expected)
 stats=[];total=0;maxerr=0
 for o in meshes:
  spec=next(m for m in recipe['meshes'] if m['name']==o.name)
  assert any(m.type=='ARMATURE' for m in o.modifiers),o.name
  weights=[[(g.group,g.weight) for g in v.groups if g.weight>1e-7] for v in o.data.vertices]
  assert all(0<len(w)<=4 for w in weights)
  err=max(abs(sum(w for _,w in ws)-1) for ws in weights);assert err<2e-6,(o.name,err)
  keys=[k.name for k in o.data.shape_keys.key_blocks if k.name!='Basis'] if o.data.shape_keys else []
  assert keys==spec['morphs'],(o.name,keys,spec['morphs'])
  xyz=coords(o.data);ref=reference[o.name+'__Basis'];tree=KDTree(len(ref))
  for i,co in enumerate(ref):tree.insert(co,i)
  tree.balance();indices=np.array([tree.find(co)[1] for co in xyz])
  error=float(np.max(np.abs(xyz-ref[indices])));assert error<2e-6,(o.name,error)
  for key in keys:
   x=np.empty(len(o.data.vertices)*3,dtype=np.float32);o.data.shape_keys.key_blocks[key].data.foreach_get('co',x)
   error=max(error,float(np.max(np.abs(x.reshape(-1,3)-reference[o.name+'__'+key][indices]))))
  assert error<2e-6,(o.name,'morph reimport deviation',error);maxerr=max(maxerr,error)
  tris=sum(len(p.vertices)-2 for p in o.data.polygons);total+=tris
  stats.append(dict(name=o.name,material=o.data.materials[0].name,materials=[m.name for m in o.data.materials],skinned=True,morph_targets=keys,vertices=len(xyz),triangles=tris,max_weight_sum_error=err,roundtrip_max_position_error_m=error))
 # Inspect the actual embedded glTF data too, rather than relying on Blender
 # quietly fixing malformed weights or material inputs during import.
 raw_total=0
 for mesh in doc['meshes']:
  if mesh.get('weights'):assert len(mesh['extras']['targetNames'])==len(mesh['weights'])
  for p in mesh['primitives']:
   assert p.get('mode',4)==4
   raw_total+=doc['accessors'][p['indices']]['count']//3
   attrs=p['attributes'];assert 'JOINTS_0' in attrs and 'WEIGHTS_0' in attrs and 'JOINTS_1' not in attrs
   weights=accessor(doc,binary,attrs['WEIGHTS_0']);assert np.isfinite(weights).all() and np.min(weights)>=0
   assert np.max(np.abs(weights.sum(axis=1)-1))<2e-6
   assert np.isfinite(accessor(doc,binary,attrs['POSITION'])).all()
 assert total==raw_total and total<=(80000 if variant=='desktop' else 30000)
 textures=[]
 for im in doc.get('images',[]):
  assert 'uri' not in im and im['mimeType']=='image/png'
  v=doc['bufferViews'][im['bufferView']];png=binary[v.get('byteOffset',0):v.get('byteOffset',0)+v['byteLength']]
  assert png[:8]==b'\x89PNG\r\n\x1a\n';w,h=struct.unpack_from('>II',png,16)
  textures.append(dict(name=im['name'],width=w,height=h,embedded=True))
 assert sorted((t['width'],t['height']) for t in textures)==sorted([(8,128),(2048,2048) if variant=='desktop' else (1024,1024)])
 material_report=[]
 for mat in doc['materials']:
  unlit='KHR_materials_unlit' in mat.get('extensions',{})
  pbr=mat.get('pbrMetallicRoughness',{});assert unlit or pbr.get('metallicFactor',1)==0
  assert unlit or pbr.get('roughnessFactor',1)==1
  used=[]
  for info in [pbr.get('baseColorTexture'),mat.get('emissiveTexture')]:
   if info:used.append(doc['images'][doc['textures'][info['index']]['source']]['name'])
  material_report.append(dict(name=mat['name'],textures=sorted(set(used)),emissive=unlit or any(mat.get('emissiveFactor',[0,0,0])),unlit=unlit,gltf=mat))
 render_names=[]
 if renders:
  setup_render(800 if variant=='desktop' else 640)
  folder=STAGE/'renders';folder.mkdir(exist_ok=True)
  poses={'neutral':{}}|{m:{m:1} for m in MORPHS}|COMBOS
  if variant=='phone':poses={n:poses[n] for n in ['neutral','eyeBlink_L','jaw_both_blinks','jaw_smile']}
  for label,values in poses.items():
   set_morphs(meshes,values)
   for view in ['front','3q']:
    filename=('' if variant=='desktop' else 'phone_')+f'face_{view}_{label}.png';render(folder/filename,view);render_names.append(filename)
  set_morphs(meshes,{})
  for view in ['front','3q']:
   filename=('' if variant=='desktop' else 'phone_')+f'full_{view}.png';render(folder/filename,view,True);render_names.append(filename)
 # Derive pitch axes in the actual exported node-local coordinate systems.
 parents={child:i for i,n in enumerate(doc['nodes']) for child in n.get('children',[])}
 world_matrices={}
 def world_matrix(i):
  if i not in world_matrices:
   n=doc['nodes'][i]
   if 'matrix' in n:local=Matrix(np.array(n['matrix']).reshape(4,4).T.tolist())
   else:
    q=n.get('rotation',[0,0,0,1]);local=Matrix.LocRotScale(Vector(n.get('translation',[0,0,0])),Quaternion((q[3],q[0],q[1],q[2])),Vector(n.get('scale',[1,1,1])))
   world_matrices[i]=(world_matrix(parents[i])@local) if i in parents else local
  return world_matrices[i]
 node_indices={n['name']:i for i,n in enumerate(doc['nodes'])}
 C=Matrix(((1,0,0,0),(0,0,1,0),(0,-1,0,0),(0,0,0,1)))
 source_rest_error=0
 for bone in recipe['skeleton']['bones']:
  actual=world_matrix(node_indices[bone['name']]);expected_matrix=C@Matrix(bone['matrix'])
  error=max(abs(actual[i][j]-expected_matrix[i][j]) for i in range(4) for j in range(4))
  source_rest_error=max(error,source_rest_error)
 assert source_rest_error<1e-5,('Source rest transforms changed',source_rest_error)
 pitch_axes={k:list(world_matrix(node_indices[n]).to_3x3().inverted()@Vector((1,0,0))) for k,n in recipe['skeleton']['humanoid'].items()}
 result=dict(local_pitch_axes=pitch_axes,name=path.name,bytes=path.stat().st_size,sha256=sha(path),triangle_count=total,texture_sizes=textures,meshes=stats,materials=material_report,
  skeleton=dict(bones=[dict(name=b.name,parent=b.parent.name if b.parent else None,rest_world_matrix_gltf=[list(row) for row in world_matrix(node_indices[b.name])]) for b in arm.data.bones]),
  source_rest_transform_max_error=source_rest_error,
  roundtrip_max_position_error_m=maxerr,renders=render_names,passed=True,input_hashes={'blend':sha(SRC)},
  height_m=float(max((o.matrix_world@v.co).z for o in meshes for v in o.data.vertices)-min((o.matrix_world@v.co).z for o in meshes for v in o.data.vertices)))
 write_json(TMP/f'verified-{variant}.json',result);print('VERIFIED',variant,total,flush=True)
 return result

DESCRIPTIONS={
 'jawOpen':'Source DEF_Jaw +0.30 rad, including SK_Jaw correction; lively speech maximum.',
 'viseme_aa':'Open ah: +0.27 rad jaw and slight corner spread.',
 'viseme_E':'Mid-open E: +0.15 rad jaw, corners drawn back and slightly up.',
 'viseme_I':'Narrow I: +0.065 rad jaw and spread corners.',
 'viseme_O':'Rounded O: +0.20 rad jaw and inward corners.',
 'viseme_U':'Small rounded U: +0.055 rad jaw and inward corners.',
 'viseme_PP':'Closed pressed lips via source upper/lower lip and corner handles.',
 'mouthSmile':'Both source corner handles spread and raised, with corrective shape keys.',
 'mouthFrown':'Both source corners lowered and drawn slightly inward.',
 'eyeBlink_L':'Left upper/lower ribbon handles meet; source spherical lid tracking baked.',
 'eyeBlink_R':'Right upper/lower ribbon handles meet; source spherical lid tracking baked.',
 'eyeSquint':'Both eyelid ribbons narrow the eyes primarily by raising the lower lid; combine with mouthSmile for happy squint.',
 'browUp':'Source eye-wrinkle/brow line handles raised 8 mm; changes eye_line, not head.',
 'browDown':'Source eye-wrinkle/brow line handles lowered 6 mm; changes eye_line, not head.'}

def manifest():
 desktop=json.loads((TMP/'verified-desktop.json').read_text());phone=json.loads((TMP/'verified-phone.json').read_text())
 for variant, record in [('desktop',desktop),('phone',phone)]:
  assert record['sha256']==sha(STAGE/record['name']),('Stale verification record',variant)
  assert record['bytes']==(STAGE/record['name']).stat().st_size
  from animate_nukorio import inputs
  animated=bool(read_glb(STAGE/record['name'])[0].get('animations'))
  expected_inputs=inputs() if animated else {'blend':sha(SRC)}
  assert record.get('input_hashes')==expected_inputs and sha(SRC)==SOURCE_SHA, 'Stale input verification'
  if animated:
   check=json.loads((TMP/f'verified-animation-{variant}.json').read_text())
   assert check['sha256']==record['sha256'] and check['inputs']==expected_inputs, 'Stale animation verification'
 recipe=json.loads((TMP/'recipe-desktop.json').read_text())
 assert bool(read_glb(STAGE/desktop['name'])[0].get('animations'))==bool(read_glb(STAGE/phone['name'])[0].get('animations')), 'Mixed animation stages'
 assert desktop['skeleton']==phone['skeleton']
 assert [(m['name'],m['material'],m['morph_targets']) for m in desktop['meshes']]==[(m['name'],m['material'],m['morph_targets']) for m in phone['meshes']]
 morphs={n:dict(description=DESCRIPTIONS[n],meshes=[m['name'] for m in desktop['meshes'] if n in m['morph_targets']],
  meshes_with_nonzero_delta=[name for name,d in recipe['capture_max_delta_m'][n].items() if d>1e-6],max_weight=1) for n in MORPHS}
 lipsync=dict(amplitude_only=dict(primary='jawOpen',range=[0,1],optional_blend={'mouthSmile':.15},smoothing_seconds=.045),
  viseme_based=dict(sil={},PP={'viseme_PP':1},FF={'viseme_I':.35,'viseme_PP':.35},TH={'viseme_E':.35},DD={'viseme_E':.4},kk={'viseme_aa':.35},CH={'viseme_O':.35,'viseme_I':.25},SS={'viseme_I':.45},nn={'viseme_I':.25},RR={'viseme_O':.5},aa={'viseme_aa':1},E={'viseme_E':1},I={'viseme_I':1},O={'viseme_O':1},U={'viseme_U':1}),
  blending='Visemes include jaw opening: crossfade them with total viseme weight <=1; do not add amplitude jawOpen to a full viseme. Blink and expression channels are separate. Unrepresented tongue/teeth phonemes use approximate snout shapes.')
 skeleton=recipe['skeleton'];skeleton['bones']=desktop['skeleton']['bones'];skeleton['local_pitch_axes']=desktop['local_pitch_axes']
 outlines=recipe['outline']
 for entry in outlines.values():
  for e in entry['entries']:e['relative_thickness']=e['thickness_m']/desktop['height_m']
 m=dict(schema_version=2,source=dict(file=str(SRC.relative_to(ROOT)),sha256=SOURCE_SHA,blender_version=bpy.app.version_string),
  files={variant:{k:r[k] for k in ['name','bytes','sha256','triangle_count','texture_sizes']} for variant,r in [('desktop',desktop),('phone',phone)]},
  coordinates=dict(up='+Y',forward='+Z',right='-X (character anatomical right)',units='meters',height_m=desktop['height_m']),rest_pose='A-pose',
  meshes=desktop['meshes'],variant_meshes={'desktop':desktop['meshes'],'phone':phone['meshes']},skeleton=skeleton,morphs=morphs,lipsync=lipsync,
  blink=dict(left='eyeBlink_L',right='eyeBlink_R',typical_duration_seconds=.14),outline=dict(meshes=outlines,meshes_without_outline=[n for n,v in outlines.items() if not any(e['enabled'] for e in v['entries'])],
   thickness_convention='Normal extrusion in source meters; relative_thickness is extrusion / imported character height. The artist GN divides Thickness by 1000 and noise Strength by 100. No outline geometry is exported.'),
  materials=desktop['materials'],verification=dict(fresh_blender_import=True,source_rest_transform_max_error=desktop['source_rest_transform_max_error'],roundtrip_max_position_error_m={v:r['roundtrip_max_position_error_m'] for v,r in [('desktop',desktop),('phone',phone)]},renders=desktop['renders'],phone_renders=phone['renders']))
 if animated:
  metadata=json.loads((TMP/'animation-metadata.json').read_text())
  assert metadata['inputs']==expected_inputs
  m['animations']=metadata['animations']
  for variant in ['desktop','phone']:
   assert [c['name'] for c in read_glb(STAGE/m['files'][variant]['name'])[0]['animations']]==[c['name'] for c in m['animations']]
 write_json(STAGE/'manifest.json',m)
 return m

def run_blender(args,logname):
 command=[bpy.app.binary_path,'-b','--factory-startup','--disable-autoexec','--python-exit-code','1','--python',str(Path(__file__).resolve()),'--']+args
 with (TMP/logname).open('w') as log:
  subprocess.run(command,env=os.environ.copy(),stdout=log,stderr=subprocess.STDOUT,check=True)

def build_all():
 from delivery import recover
 recover(OUT,TMP)
 from animate_nukorio import inputs
 inputs()
 assert sha(SRC)==SOURCE_SHA
 if STAGE.exists():shutil.rmtree(STAGE)
 STAGE.mkdir()
 for variant in ['desktop','phone']:
  run_blender(['--variant',variant],f'build-{variant}.log')
  run_blender(['--verify',variant],f'verify-{variant}.log')
 manifest()
 node=shutil.which('node');assert node,'Node 24 required for three.js verification'
 with (TMP/'threejs.log').open('w') as log:subprocess.run([node,str(PIPE/'verify_threejs.mjs')],env=os.environ.copy(),stdout=log,stderr=subprocess.STDOUT,check=True)
 report=json.loads((TMP/'threejs_verification.json').read_text());m=json.loads((STAGE/'manifest.json').read_text());m['verification']['threejs']=report
 m['skeleton']['threejs_runtime_names']=report['variants']['desktop']['bone_runtime_names']
 m['skeleton']['threejs_binding_note']='GLTFLoader sanitizes punctuation: e.g. head.x becomes headx. Look up by bone.userData.name (original GLB name), or use this runtime-name map; bind AnimationMixer tracks by UUID.'
 write_json(STAGE/'manifest.json',m)
 # Calibrated GLB bytes have been visually reviewed; a drift requires review.
 baseline_path=PIPE/'visual-baseline.json'
 assert baseline_path.exists(),'Review staging renders before creating visual-baseline.json; nothing promoted.'
 baseline=json.loads(baseline_path.read_text())
 assert baseline['source_sha256']==SOURCE_SHA
 for v in ['desktop','phone']:assert m['files'][v]['sha256']==baseline['files'][v],('Visual calibration drift',v)
 m['verification']['visual_review']=dict(baseline='asset-pipeline/nukorio/visual-baseline.json',reviewed_glb_hashes_match=True,reviewed_renders=baseline['reviewed_renders'],notes=baseline['review_notes'])
 write_json(STAGE/'manifest.json',m)
 from animation_pipeline import finish
 finish()
 print('BUILD COMPLETE',OUT,flush=True)

if __name__=='__main__':
 parser=argparse.ArgumentParser();parser.add_argument('--variant',choices=['desktop','phone']);parser.add_argument('--verify',choices=['desktop','phone']);parser.add_argument('--manifest',action='store_true');parser.add_argument('--probe',action='store_true')
 args=parser.parse_args(sys.argv[sys.argv.index('--')+1:] if '--' in sys.argv else [])
 STAGE.mkdir(parents=True,exist_ok=True)
 if args.variant:build_variant(args.variant)
 elif args.verify:verify_variant(args.verify)
 elif args.manifest:manifest()
 elif args.probe:
  a=open_source();configure(1);setup_render(512)
  for name in ['neutral']+MORPHS:
   pose(a,name);render(TMP/('source_'+name+'.png'))
 else:build_all()
