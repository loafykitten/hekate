"""Offline Mixamo sampling, proportion-aware retargeting and lossless GLB animation append."""
import os
from pathlib import Path
PIPE=Path(__file__).resolve().parent
TMP=PIPE/'tmp';TMP.mkdir(exist_ok=True)
os.environ['TMPDIR']=str(TMP)
import tempfile,sys
sys.dont_write_bytecode=True;tempfile.tempdir=str(TMP)
sys.path.insert(0,str(PIPE))
import build_nukorio as b
import bpy, numpy as np, json, math, struct, copy
from mathutils import Matrix,Vector,Quaternion
bpy.context.preferences.filepaths.temporary_directory=str(TMP)
CLIPS=[('idle','Idle.fbx',121,True,'idle'),('idle_2','Idle-2.fbx',251,True,'idle'),('talking','Talking.fbx',156,True,'talk'),('waving','Waving.fbx',143,False,'gesture'),('thankful','Thankful.fbx',91,False,'gesture'),('acknowledging','Acknowledging.fbx',59,False,'gesture'),('dismissing','Dismissing Gesture.fbx',68,False,'gesture'),('excited','Excited.fbx',198,False,'gesture'),('no','No.fbx',151,False,'gesture'),('smh','Smh.fbx',220,False,'gesture'),('standing_pose','Female Standing Pose.fbx',2,True,'pose')]
C=Matrix(((1,0,0),(0,0,1),(0,-1,0))).to_quaternion()

def inputs():
 hashes=json.loads((PIPE/'mixamo-inputs.json').read_text())
 assert set(hashes)=={x[1] for x in CLIPS}
 assert b.sha(b.SRC)==b.SOURCE_SHA
 for f,h in hashes.items():assert b.sha(b.ROOT/'mixamo'/f)==h,('Mixamo input mismatch',f)
 return {'blend':b.SOURCE_SHA,**hashes}

def load(f):
 b.clear();bpy.ops.import_scene.fbx(filepath=str(b.ROOT/'mixamo'/f),use_anim=True)
 return next(o for o in bpy.context.scene.objects if o.type=='ARMATURE')

def smooth(t):return t*t*(3-2*t)

def retarget():
 hashes=inputs();a=load('Idle.fbx')
 source={x.name.removeprefix('mixamorig:'):(a.matrix_world@x.matrix_local) for x in a.data.bones}
 recipe=json.loads((TMP/'recipe-desktop.json').read_text())['skeleton']
 rest={x['name']:Matrix(x['matrix']) for x in recipe['bones']};parents={x['name']:x['parent'] for x in recipe['bones']}
 order=[]
 def visit(n):
  if n in order:return
  if parents[n]:visit(parents[n])
  order.append(n)
 for n in rest:visit(n)
 rq={n:m.to_quaternion() for n,m in rest.items()};rp={n:m.translation for n,m in rest.items()}
 offsets={n:rq[parents[n]].inverted()@(rp[n]-rp[parents[n]]) for n in rest if parents[n]}
 mapping={'c_root_bend.x':'Hips','c_spine_01_bend.x':'Spine','c_spine_02_bend.x':'Spine1','c_spine_03_bend.x':'Spine2','neck.x':'Neck','head.x':'Head'}
 calibration={n:Quaternion() for n in rest}
 for s,side in [('l','Left'),('r','Right')]:
  for target,src in [('shoulder','Shoulder'),('arm_stretch','Arm'),('forearm_stretch','ForeArm'),('hand','Hand')]:mapping[target+'.'+s]=side+src
  # Bring A-rest arm/hand into the source's T-rest, preserving target bone roll.
  axis=Vector((1 if s=='l' else -1,0,0))
  for n in ['arm_stretch.'+s,'forearm_stretch.'+s,'hand.'+s]:calibration[n]=(rq[n]@Vector((0,1,0))).rotation_difference(axis)
  for d in ['thumb','index','pinky']:
   for i,n in enumerate([d+'1.'+s,'c_'+d+'2.'+s,'c_'+d+'3.'+s]):
    mapping[n]=side+'Hand'+d.title()+str(i+1);calibration[n]=calibration['hand.'+s]
 for s in ['l','r']:
  for n in ['thigh_b_str','thigh_stretch','leg_stretch','foot','toes_01']:mapping[n+'.'+s]=None
 lengths={s:[(rp[y+'.'+s]-rp[x+'.'+s]).length for x,y in zip(['thigh_b_str','thigh_stretch','leg_stretch'],['thigh_stretch','leg_stretch','foot'])] for s in ['l','r']}
 scale=sum(lengths['l'])/((source['LeftLeg'].translation-source['LeftUpLeg'].translation).length+(source['LeftFoot'].translation-source['LeftLeg'].translation).length)
 tracks={};stats={}
 for name,file,count,loop,cat in CLIPS:
  a=load(file);frames=[];raw=[]
  for f in range(1,count+1):
   bpy.context.scene.frame_set(f)
   raw.append({n:(a.matrix_world@a.pose.bones['mixamorig:'+n].matrix) for n in source})
  z0=raw[0]['Hips'].translation.z
  stance_lowering=.025+max(0,max(scale*(x['Hips'].translation.z-z0) for x in raw)-.017)
  for f,pose in enumerate(raw):
   q={};p={};desired={}
   for n,sn in mapping.items():
    if sn:desired[n]=pose[sn].to_quaternion()@source[sn].to_quaternion().inverted()@calibration[n]@rq[n]
   root=rp['c_root_bend.x'].copy();root.z+=scale*(pose['Hips'].translation.z-z0)-stance_lowering
   # Never translate horizontally. A small stance crouch reserves IK extension.
   def fk():
    for n in order:
     par=parents[n]
     q[n]=desired.get(n,(q[par]@rq[par].inverted()@rq[n]) if par else rq[n])
     p[n]=p[par]+q[par]@offsets[n] if par else root.copy()
   fk()
   for s in ['l','r']:
    # Rest-shape-seeded three-link FABRIK distributes bend across knee and hock.
    names=[n+'.'+s for n in ['thigh_b_str','thigh_stretch','leg_stretch','foot']]
    points=[p[names[0]].copy()]
    side='Left' if s=='l' else 'Right'
    for i in range(3):
     sn=side+('UpLeg' if i==0 else 'Leg')
     delta=pose[sn].to_quaternion()@source[sn].to_quaternion().inverted()
     delta=Quaternion().slerp(delta,.4 if i==0 else .25)
     points.append(points[-1]+delta@(rp[names[i+1]]-rp[names[i]]))
    anchor=p[names[0]].copy();goal=rp[names[-1]].copy();ls=lengths[s]
    for _ in range(32):
     points[-1]=goal.copy()
     for i in range(2,-1,-1):points[i]=points[i+1]+(points[i]-points[i+1]).normalized()*ls[i]
     points[0]=anchor.copy()
     for i in range(3):points[i+1]=points[i]+(points[i+1]-points[i]).normalized()*ls[i]
    for i in range(3):desired[names[i]]=(rp[names[i+1]]-rp[names[i]]).rotation_difference(points[i+1]-points[i])@rq[names[i]]
    desired[names[-1]]=rq[names[-1]];desired['toes_01.'+s]=rq['toes_01.'+s]
   fk()
   # Keep broad sleeves and paws in front of torso/fur and away from the face.
   for s in ['l','r']:
    upper,fore,hand=[n+'.'+s for n in ['arm_stretch','forearm_stretch','hand']]
    shoulder=p[upper];elbow=p[fore];wrist=p[hand];goal=wrist.copy()
    if .96<goal.z<1.59 and abs(goal.x)<.29:goal.y=min(goal.y,-.36)
    center=p['head.x']+q['head.x']@Vector((0,.02,.03));radii=Vector((.29,.39,.30));d=goal-center
    ell=sum((d[i]/radii[i])**2 for i in range(3))
    if ell<1:
     goal.y=min(goal.y,center.y-radii.y*math.sqrt(max(.05,1-(d.x/radii.x)**2-(d.z/radii.z)**2)))
    # Analytic two-link reach, preserving the source elbow plane.
    u=(rp[fore]-rp[upper]).length;v=(rp[hand]-rp[fore]).length
    ray=goal-shoulder;dist=min(ray.length,u+v-.0001);axis=ray.normalized();goal=shoulder+axis*dist
    along=(u*u-v*v+dist*dist)/(2*dist);height=math.sqrt(max(0,u*u-along*along))
    bend=(elbow-shoulder)-axis*(elbow-shoulder).dot(axis)
    if bend.length<1e-5:bend=Vector((1 if s=='l' else -1,-.2,0))
    mid=shoulder+axis*along+bend.normalized()*height
    desired[upper]=(q[upper]@(rq[upper].inverted()@(rp[fore]-rp[upper]))).rotation_difference(mid-shoulder)@q[upper]
    desired[fore]=(q[fore]@(rq[fore].inverted()@(rp[hand]-rp[fore]))).rotation_difference(goal-mid)@q[fore]
   fk()
   local={n:(q[parents[n]].inverted()@q[n] if parents[n] else C@q[n]) for n in mapping}
   frames.append({'q':local,'p':C@root})
  if name=='standing_pose':frames[1]=copy.deepcopy(frames[0])
  if loop and count>2:
   # Preserve the opening motion; ease the last 0.5 seconds onto the same pose.
   width=min(15,count//4)
   for i in range(count-width,count):
    w=smooth((i-(count-width))/(width-1))
    for n in mapping:frames[i]['q'][n]=frames[i]['q'][n].slerp(frames[0]['q'][n],w)
    frames[i]['p']=frames[i]['p'].lerp(frames[0]['p'],w)
  if not loop:
   # 0.4-second bookends end exactly on idle; short gestures retain their middle.
   idle=tracks['idle'][0];width=min(12,count//4)
   for i in range(width):
    w=smooth(i/(width-1))
    for j in [i,count-1-i]:
     for n in mapping:frames[j]['q'][n]=idle['q'][n].slerp(frames[j]['q'][n],w)
     frames[j]['p']=idle['p'].lerp(frames[j]['p'],w)
  # Re-solving legs after transition blends keeps end-effector contact exact.
  for fr in frames:
   wq={};wp={}
   for n in order:
    par=parents[n];lq=fr['q'].get(n,rq[par].inverted()@rq[n] if par else C@rq[n])
    wq[n]=wq[par]@lq if par else C.inverted()@lq
    wp[n]=wp[par]+wq[par]@offsets[n] if par else C.inverted()@fr['p']
   for s in ['l','r']:
    ns=[n+'.'+s for n in ['thigh_b_str','thigh_stretch','leg_stretch','foot']];pt=[wp[n].copy() for n in ns];anchor=pt[0].copy();goal=rp[ns[-1]];ls=lengths[s]
    for _ in range(40):
     pt[-1]=goal.copy()
     for i in range(2,-1,-1):pt[i]=pt[i+1]+(pt[i]-pt[i+1]).normalized()*ls[i]
     pt[0]=anchor.copy()
     for i in range(3):pt[i+1]=pt[i]+(pt[i+1]-pt[i]).normalized()*ls[i]
    for i,n in enumerate(ns):
     wq[n]=(rp[ns[i+1]]-rp[n]).rotation_difference(pt[i+1]-pt[i])@rq[n] if i<3 else rq[n]
     fr['q'][n]=wq[parents[n]].inverted()@wq[n]
    n='toes_01.'+s;fr['q'][n]=wq[parents[n]].inverted()@rq[n]
  tracks[name]=frames
  stats[name]={'frames':count,'duration_seconds':(count-1)/30,'name':name,'file':'mixamo/'+file,'source_sha256':hashes[file],'loop':loop,'category':cat,'head_motion':'strong' if name in ['idle','acknowledging','no','smh','thankful','excited','talking'] else 'moderate','description':f'Mixamo {name.replace("_"," ")}; fixed foot contacts, centered hips, three-digit hands.'}
  print('RETARGET',name,flush=True)
 b.write_json(TMP/'animation-metadata.json',{'inputs':hashes,'animations':list(stats.values()),'leg_scale':scale})
 return tracks

def reduce_keys(values,quat,tolerance):
 # Ramer-Douglas-Peucker with the same linear/spherical interpolation as glTF.
 keep={0,len(values)-1}
 def visit(lo,hi):
  if hi-lo<=1:return
  errors=[]
  for i in range(lo+1,hi):
   t=(i-lo)/(hi-lo)
   if quat:
    v=values[lo].slerp(values[hi],t);error=2*math.acos(min(1,abs(v.dot(values[i]))))
   else:error=(values[lo].lerp(values[hi],t)-values[i]).length
   errors.append(error)
  error=max(errors)
  if error>tolerance:
   i=lo+1+errors.index(error);keep.add(i);visit(lo,i);visit(i,hi)
 visit(0,len(values)-1)
 return sorted(keep)

def append(tracks,variant):
 path=b.STAGE/f'renamon-{variant}.glb';doc,original=b.read_glb(path)
 assert not doc.get('animations'),'Animation stage expects calibrated round-2 input'
 baseline=json.loads((PIPE/'visual-baseline.json').read_text())
 assert b.sha(path)==baseline['files'][variant]
 data=bytearray(original);doc['animations']=[];indices={n['name']:i for i,n in enumerate(doc['nodes'])}
 def add(values,kind):
  while len(data)%4:data.append(0)
  arr=np.asarray(values,dtype='<f4');offset=len(data);data.extend(arr.tobytes());view=len(doc['bufferViews']);doc['bufferViews'].append({'buffer':0,'byteOffset':offset,'byteLength':arr.nbytes})
  acc={'bufferView':view,'componentType':5126,'count':len(values),'type':kind}
  if kind=='SCALAR':acc.update(min=[float(arr.min())],max=[float(arr.max())])
  idx=len(doc['accessors']);doc['accessors'].append(acc);return idx
 for name,frames in tracks.items():
  anim={'name':name,'channels':[],'samplers':[]}
  for n in frames[0]['q']:
   vals=[f['q'][n].normalized() for f in frames]
   for i in range(1,len(vals)):
    if vals[i-1].dot(vals[i])<0:vals[i].negate()
   # 0.05 degrees max local angular error; <1 mm at an arm's reach.
   ids=reduce_keys(vals,True,math.radians(.05))
   inp=add([i/30 for i in ids],'SCALAR');out=add([[vals[i].x,vals[i].y,vals[i].z,vals[i].w] for i in ids],'VEC4')
   anim['channels'].append({'sampler':len(anim['samplers']),'target':{'node':indices[n],'path':'rotation'}});anim['samplers'].append({'input':inp,'output':out,'interpolation':'LINEAR'})
  vals=[f['p'] for f in frames];ids=reduce_keys(vals,False,.00005)
  inp=add([i/30 for i in ids],'SCALAR');out=add([list(vals[i]) for i in ids],'VEC3')
  anim['channels'].append({'sampler':len(anim['samplers']),'target':{'node':indices['c_root_bend.x'],'path':'translation'}});anim['samplers'].append({'input':inp,'output':out,'interpolation':'LINEAR'})
  doc['animations'].append(anim)
 doc['buffers'][0]['byteLength']=len(data)
 js=json.dumps(doc,separators=(',',':')).encode();js+=b' '*((-len(js))%4);data+=b'\0'*((-len(data))%4)
 path.write_bytes(struct.pack('<4sII',b'glTF',2,12+8+len(js)+8+len(data))+struct.pack('<I4s',len(js),b'JSON')+js+struct.pack('<I4s',len(data),b'BIN\0')+data)
 print('APPENDED',variant,path.stat().st_size,flush=True)

def build():
 tracks=retarget()
 for v in ['desktop','phone']:append(tracks,v)

if __name__=='__main__':build()
