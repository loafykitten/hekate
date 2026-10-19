"""Recoverable two-rename promotion. All scratch remains under the pipeline tmp."""
import json,shutil

def recover(out,tmp):
 previous=tmp/'previous-delivery';journal=tmp/'promotion-state.json'
 if not out.exists() and previous.exists():previous.rename(out)
 if journal.exists():
  state=json.loads(journal.read_text())
  if state['phase']=='moving' and not out.exists():raise RuntimeError('Interrupted promotion has no recoverable delivery')
  journal.unlink()

def promote(stage,out,tmp,validate):
 recover(out,tmp)
 promotion=tmp/'promotion';previous=tmp/'previous-delivery';journal=tmp/'promotion-state.json'
 if promotion.exists():shutil.rmtree(promotion)
 shutil.copytree(stage,promotion);validate(promotion)
 if previous.exists():shutil.rmtree(previous)
 journal.write_text(json.dumps({'phase':'moving'})+'\n')
 moved=False
 try:
  if out.exists():out.rename(previous);moved=True
  promotion.rename(out)
  validate(out)
 except BaseException:
  if moved:
   if out.exists():
    failed=tmp/'failed-promotion'
    if failed.exists():shutil.rmtree(failed)
    out.rename(failed)
   previous.rename(out)
  raise
 finally:
  if out.exists() and journal.exists():journal.unlink()
