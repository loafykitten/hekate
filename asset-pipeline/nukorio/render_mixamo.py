"""Render source motion on Idle or the gesture pack X Bot for comparison."""
import os
from pathlib import Path
P=Path(__file__).resolve().parent;os.environ['TMPDIR']=str(P/'tmp')
import tempfile,sys
sys.dont_write_bytecode=True;tempfile.tempdir=str(P/'tmp');sys.path.insert(0,str(P))
import animate_nukorio as a
import build_nukorio as b
import bpy,numpy as np
from mathutils import Vector
gestures='--gestures' in sys.argv
folder=b.TMP/('source-gestures' if gestures else 'source-motion');folder.mkdir(exist_ok=True)
a.inputs()
for name,file,count,loop,cat in (a.CLIPS[len(a.ROUND3_CLIPS):] if gestures else a.ROUND3_CLIPS):
 src=a.load(file);samples=[]
 for i in range(5):
  f=1+(count-1)*i/4;bpy.context.scene.frame_set(int(f),subframe=f-int(f));samples.append({p.name:src.matrix_world@p.matrix for p in src.pose.bones})
 rig=a.load(a.PACK+'!X Bot.fbx' if gestures else 'Idle.fbx');rig.animation_data_clear();scene=b.setup_render(360);scene.render.resolution_x=300
 for i,pose in enumerate(samples):
  for bone in rig.pose.bones:
   wanted=rig.matrix_world.inverted()@pose[bone.name]
   rest=bone.bone.matrix_local
   if bone.parent:
    wanted=(rig.matrix_world.inverted()@pose[bone.parent.name]).inverted()@wanted
    rest=bone.parent.bone.matrix_local.inverted()@rest
   bone.matrix_basis=rest.inverted()@wanted
  bpy.context.view_layer.update()
  for view in ['front','3q']:
   cam=scene.camera;target=Vector((0,0,.95));direction=Vector((4,-5,.35) if view=='3q' else (0,-5,.1));cam.location=target+direction;cam.rotation_euler=(-direction).to_track_quat('-Z','Y').to_euler();cam.data.ortho_scale=2.6
   scene.render.filepath=str(folder/f'{name}_{view}_{i}.png');bpy.ops.render.render(write_still=True)
 sheet=np.zeros((720,1500,4),dtype=np.float32)
 for row,view in enumerate(['3q','front']):
  for i in range(5):
   im=bpy.data.images.load(str(folder/f'{name}_{view}_{i}.png'));arr=np.empty(360*300*4,dtype=np.float32);im.pixels.foreach_get(arr);sheet[row*360:(row+1)*360,i*300:(i+1)*300]=arr.reshape(360,300,4);bpy.data.images.remove(im)
 im=bpy.data.images.new(name,width=1500,height=720,alpha=True);im.pixels.foreach_set(sheet.ravel());im.filepath_raw=str(folder/f'{name}_contact.png');im.file_format='PNG';im.save();bpy.data.images.remove(im)

from deterministic_png import normalize_folder
normalize_folder(folder)
