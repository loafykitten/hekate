"""Assemble unaltered Blender frames into two-row contact sheets (front, three-quarter)."""
import os
from pathlib import Path
P=Path(__file__).resolve().parent;os.environ['TMPDIR']=str(P/'tmp')
import tempfile,sys
sys.dont_write_bytecode=True;tempfile.tempdir=str(P/'tmp')
import bpy,numpy as np,json
bpy.context.preferences.filepaths.temporary_directory=str(P/'tmp')
folder=P/'tmp/staging/renders'
for spec in json.loads((P/'tmp/animation-metadata.json').read_text())['animations']:
 name=spec['name'];pixels=np.zeros((720,1500,4),dtype=np.float32)
 for row,view in enumerate(['3q','front']):
  for i in range(5):
   im=bpy.data.images.load(str(folder/f'animation_{name}_{view}_{i}.png'));arr=np.empty(360*300*4,dtype=np.float32);im.pixels.foreach_get(arr);pixels[row*360:(row+1)*360,i*300:(i+1)*300]=arr.reshape(360,300,4);bpy.data.images.remove(im)
 im=bpy.data.images.new(name,width=1500,height=720,alpha=True);im.pixels.foreach_set(pixels.ravel());im.filepath_raw=str(folder/f'animation_{name}_contact.png');im.file_format='PNG';im.save();bpy.data.images.remove(im)
