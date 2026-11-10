ALTER TABLE tasks ADD COLUMN parent_id TEXT REFERENCES tasks(id) ON DELETE SET NULL;
ALTER TABLE tasks ADD COLUMN result_size INTEGER NOT NULL DEFAULT 0;
CREATE INDEX tasks_parent ON tasks(parent_id, target);

UPDATE tasks
SET result_size=CAST(REPLACE(REPLACE(result,'已下载并生成预览，',''),' 字节','') AS INTEGER)
WHERE kind='download' AND result GLOB '已下载并生成预览，[0-9]* 字节';

-- Old automatic downloads have no reliable originating request.
INSERT INTO tasks(id,kind,target,label,status,progress,attempts,result,created_at,updated_at)
SELECT 'legacy-auto-downloads','download','legacy-auto-downloads','历史自动下载',
    'succeeded',100,1,'历史自动下载记录',MIN(created_at),MAX(updated_at)
FROM tasks WHERE kind='download' AND label='自动下载媒体' HAVING COUNT(*)>0;

UPDATE tasks SET parent_id='legacy-auto-downloads'
WHERE kind='download' AND label='自动下载媒体';
