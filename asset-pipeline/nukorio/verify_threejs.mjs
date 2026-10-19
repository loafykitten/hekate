// Offline GLTFLoader / morph / AnimationMixer / CPU skinning verification.
// The PNG shim checks embedded dimensions, not GPU rendering.
import fs from 'node:fs/promises';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import * as THREE from '../../app/node_modules/three/build/three.module.js';
import { GLTFLoader } from '../../app/node_modules/three/examples/jsm/loaders/GLTFLoader.js';
globalThis.self = globalThis;
globalThis.ProgressEvent = class { constructor(type, properties) { this.type = type; Object.assign(this, properties); } };
globalThis.createImageBitmap = async blob => {
  const data = Buffer.from(await blob.arrayBuffer());
  assert.equal(data.subarray(0, 8).toString('hex'), '89504e470d0a1a0a');
  return { width: data.readUInt32BE(16), height: data.readUInt32BE(20), close() {} };
};
const base = new URL('./tmp/staging/', import.meta.url);
const manifest = JSON.parse(await fs.readFile(new URL('manifest.json', base), 'utf8'));
const variants = {};
for (const variant of ['desktop', 'phone']) {
  const bytes = await fs.readFile(new URL(`renamon-${variant}.glb`, base));
  const digest = crypto.createHash('sha256').update(bytes).digest('hex');
  assert.equal(digest, manifest.files[variant].sha256);
  const gltf = await new GLTFLoader().parseAsync(bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength), '');
  const meshes = [];
  gltf.scene.traverse(o => { if (o.isMesh) meshes.push(o); });
  assert.equal(meshes.length, manifest.meshes.length);
  assert(meshes.every(m => m.isSkinnedMesh));
  assert.equal(gltf.animations.length, manifest.animations?.length ?? 0);
  const head = gltf.scene.getObjectByName('head');
  let headBone;
  gltf.scene.traverse(o => { if (o.isBone && o.userData.name === 'head.x') headBone = o; });
  assert(head && headBone?.isBone);
  assert.deepEqual(Object.keys(head.morphTargetDictionary), Object.keys(manifest.morphs).sort((a, b) => head.morphTargetDictionary[a] - head.morphTargetDictionary[b]));
  const update = () => { gltf.scene.updateMatrixWorld(true); for (const m of meshes) m.skeleton.update(); };
  const point = (m, i) => m.getVertexPosition(i, new THREE.Vector3()).applyMatrix4(m.matrixWorld);
  update();
  const rest = new Map(meshes.map(m => [m.name, Array.from({length:m.geometry.attributes.position.count}, (_, i) => point(m, i))]));
  // At rest, standard inverse-bind skinning must preserve the stored geometry.
  for (const m of meshes) {
    for (let i=0;i<m.geometry.attributes.position.count;i+=13) {
      const basePoint=new THREE.Vector3().fromBufferAttribute(m.geometry.attributes.position,i).applyMatrix4(m.matrixWorld);
      assert(basePoint.distanceTo(rest.get(m.name)[i])<2e-6);
    }
  }
  const morphChecks = {};
  for (const name of Object.keys(manifest.morphs)) {
    const changed = {};
    for (const m of meshes) {
      const index = m.morphTargetDictionary?.[name];
      if (index === undefined) continue;
      m.morphTargetInfluences[index] = 1;
      let max = 0;
      for (let i = 0; i < m.geometry.attributes.position.count; i++) {
        const p = point(m, i);
        assert(p.toArray().every(Number.isFinite));
        max = Math.max(max, p.distanceTo(rest.get(m.name)[i]));
      }
      if (max > 1e-6) changed[m.name] = max;
      m.morphTargetInfluences[index] = 0;
    }
    assert(Object.keys(changed).length > 0, `${name} must move actual vertices`);
    if (!name.startsWith('brow')) assert(changed.head > 1e-6);
    else assert(changed.eye_line > .001);
    morphChecks[name] = changed;
  }
  const q0 = headBone.quaternion.clone();
  const axis = new THREE.Vector3(...manifest.skeleton.local_pitch_axes.head).normalize();
  const q1 = q0.clone().multiply(new THREE.Quaternion().setFromAxisAngle(axis, .2));
  // UUID binding is unambiguous for a source bone whose name contains a dot.
  const track = new THREE.QuaternionKeyframeTrack(`${headBone.uuid}.quaternion`, [0, 1], [...q0.toArray(), ...q1.toArray()]);
  const mixer = new THREE.AnimationMixer(gltf.scene);
  const action = mixer.clipAction(new THREE.AnimationClip('head.x_nod', 1, [track]));
  action.setLoop(THREE.LoopOnce, 1); action.clampWhenFinished = true; action.play();
  mixer.setTime(.5); update();
  assert(headBone.quaternion.angleTo(q0) > .09);
  let moved = 0;
  for (let i = 0; i < head.geometry.attributes.position.count; i++) moved = Math.max(moved, point(head, i).distanceTo(rest.get('head')[i]));
  assert(moved > .01, 'AnimationMixer head track must move skinned head vertices');
  for (const m of meshes) {
    assert(m.skeleton.boneMatrices.every(Number.isFinite));
    const weights = m.geometry.attributes.skinWeight;
    assert.equal(weights.itemSize, 4);
    for (let i = 0; i < weights.count; i++) {
      const sum = [0,1,2,3].reduce((a,k) => a + weights.getComponent(i,k), 0);
      assert(Math.abs(sum - 1) < 2e-6);
      if (i % 37 === 0) {
        const p = point(m, i);
        assert(p.toArray().every(Number.isFinite) && p.length() < 4);
      }
    }
  }
  mixer.stopAllAction(); headBone.quaternion.copy(q0); update();
  const animationChecks = {};
  const warnings = [];
  const originalWarn = console.warn;
  console.warn = (...args) => { warnings.push(args.join(' ')); originalWarn(...args); };
  const bones = head.skeleton.bones;
  const feet = bones.filter(b => ['foot.l','foot.r','toes_01.l','toes_01.r'].includes(b.userData.name));
  const soleSamples = [];
  for (const m of meshes) {
    if (m.name !== 'body') continue;
    for (let i=0;i<m.geometry.attributes.position.count;i++) {
      if (rest.get(m.name)[i].y < .025) soleSamples.push([m,i,rest.get(m.name)[i].clone()]);
    }
  }
  let idlePose;
  for (const clip of gltf.animations) {
    const spec = manifest.animations.find(a => a.name === clip.name);
    assert(spec);
    assert.equal(typeof spec.idle_variation, 'boolean');
    assert.equal(spec.idle_variation, ['look_away','relieved_sigh','weight_shift'].includes(clip.name));
    assert(Math.abs(clip.duration-spec.duration_seconds)<1e-5);
    for (const track of clip.tracks) {
      const parsed = THREE.PropertyBinding.parseTrackName(track.name);
      const target = THREE.PropertyBinding.findNode(gltf.scene, parsed.nodeName);
      assert(target?.isBone, `Unresolved animation track ${track.name}`);
      assert(!/Ear|spline|DEF_Lip|Eyelid/.test(target.userData.name));
      assert(['quaternion','position'].includes(parsed.propertyName));
      if(parsed.propertyName==='position') {
        assert.equal(target.userData.name,'c_root_bend.x');
        for(let i=0;i<track.values.length;i+=3) {
          assert(Math.abs(track.values[i]-track.values[0])<1e-7);
          assert(Math.abs(track.values[i+2]-track.values[2])<1e-7);
        }
      }
    }
    const action=mixer.clipAction(clip);
    action.reset().setLoop(THREE.LoopOnce,1);action.clampWhenFinished=true;action.play();
    const poses=[], contacts=[];let motion=0,maxSoleDrift=0,minSoleY=Infinity,maxSoleDrop=0;
    let firstHead, previousHead, headExcursion=0, headStep=0;
    for(let i=0;i<=Math.ceil(clip.duration*60);i++) {
      const t=Math.min(clip.duration,i/60);mixer.setTime(t);update();
      const headQ=headBone.getWorldQuaternion(new THREE.Quaternion());
      firstHead ??= headQ.clone();
      headExcursion=Math.max(headExcursion,headQ.angleTo(firstHead));
      if(previousHead) headStep=Math.max(headStep,headQ.angleTo(previousHead));
      previousHead=headQ;
      const pose=bones.map(b=>[...b.position.toArray(),...b.quaternion.toArray()]);
      if(i===0 || t===clip.duration) poses.push(pose);
      if(i>0) for(let k=0;k<bones.length;k++) motion=Math.max(motion, ...pose[k].map((v,j)=>Math.abs(v-poses[0][k][j])));
      contacts.push(feet.map(b=>b.getWorldPosition(new THREE.Vector3())));
      for(const [m,index,restP] of soleSamples) {
        const p=point(m,index);maxSoleDrop=Math.max(maxSoleDrop,restP.y-p.y);minSoleY=Math.min(minSoleY,p.y);maxSoleDrift=Math.max(maxSoleDrift,p.distanceTo(restP));
      }
      for(const b of bones) assert(b.matrixWorld.elements.every(Number.isFinite));
    }
    let footDrift=0;
    for(const sample of contacts) for(let j=0;j<feet.length;j++) footDrift=Math.max(footDrift,sample[j].distanceTo(contacts[0][j]));
    assert(footDrift<.002, `${clip.name} foot drift ${footDrift}`);
    assert(maxSoleDrop<.001, `${clip.name} sole drop ${maxSoleDrop}`);
    assert(maxSoleDrift<.003, `${clip.name} sole drift ${maxSoleDrift}`);
    if(spec.category!=='pose') assert(motion>.001, `${clip.name} must move`);
    const last=poses.at(-1);let seam=0;
    if(clip.name==='idle') idlePose=poses[0];
    if(!spec.loop) for(const pose of [poses[0],last]) for(let k=0;k<bones.length;k++) {
      assert(new THREE.Vector3(...pose[k].slice(0,3)).distanceTo(new THREE.Vector3(...idlePose[k].slice(0,3)))<1e-5);
      assert(new THREE.Quaternion(...pose[k].slice(3)).angleTo(new THREE.Quaternion(...idlePose[k].slice(3)))<.001);
    }
    for(let k=0;k<bones.length;k++) for(let j=0;j<7;j++) seam=Math.max(seam,Math.abs(last[k][j]-poses[0][k][j]));
    if(spec.loop) assert(seam<1e-5, `${clip.name} loop seam ${seam}`);
    animationChecks[clip.name]={tracks:clip.tracks.length,duration_seconds:clip.duration,sampled_fps:60,head_excursion_degrees:THREE.MathUtils.radToDeg(headExcursion),max_head_step_degrees_at_60hz:THREE.MathUtils.radToDeg(headStep),max_bone_motion:motion,loop_seam_max_component:seam,foot_drift_m:footDrift,sole_drift_m:maxSoleDrift,minimum_sole_y_m:minSoleY,max_sole_drop_from_rest_m:maxSoleDrop,passed:true};
    mixer.stopAllAction();update();
  }
  console.warn=originalWarn;
  assert.equal(warnings.length,0,'Animation binding warnings');
  variants[variant] = {sha256:digest, animations:animationChecks, skinned_meshes:meshes.length, bones:head.skeleton.bones.length, morphs:morphChecks,
    bone_runtime_names:Object.fromEntries(head.skeleton.bones.map(b=>[b.userData.name,b.name])), head_runtime_name:headBone.name, source_bone_name:headBone.userData.name, head_mixer_rotation_radians:.1, head_mixer_max_vertex_motion_m:moved, passed:true};
}
const report = {three_revision:THREE.REVISION, variants, passed:true,
  scope:'Real GLTFLoader, AnimationMixer and CPU morph/skin evaluation; embedded PNG header shim. Blender EEVEE renders provide visual checks; no app UI or WebGL rendering was exercised.'};
await fs.writeFile(new URL('./tmp/threejs_verification.json', import.meta.url), JSON.stringify(report, null, 2) + '\n');
console.log(JSON.stringify(report, null, 2));
