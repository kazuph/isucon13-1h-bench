CREATE TABLE user_stats (
  user_id BIGINT NOT NULL PRIMARY KEY,
  user_name VARBINARY(1020) NOT NULL,
  reaction_count BIGINT NOT NULL DEFAULT 0,
  comment_count BIGINT NOT NULL DEFAULT 0,
  total_tip BIGINT NOT NULL DEFAULT 0,
  score BIGINT GENERATED ALWAYS AS (reaction_count + total_tip) STORED,
  INDEX ranking (score, user_name)
) ENGINE=InnoDB;
CREATE TABLE stream_stats (
  livestream_id BIGINT NOT NULL PRIMARY KEY,
  reaction_count BIGINT NOT NULL DEFAULT 0,
  comment_count BIGINT NOT NULL DEFAULT 0,
  total_tip BIGINT NOT NULL DEFAULT 0,
  score BIGINT GENERATED ALWAYS AS (reaction_count + total_tip) STORED,
  INDEX ranking (score, livestream_id)
) ENGINE=InnoDB;

INSERT INTO stream_stats(livestream_id,reaction_count,comment_count,total_tip)
SELECT l.id,COALESCE(r.n,0),COALESCE(c.n,0),COALESCE(c.tip,0)
FROM livestreams l
LEFT JOIN (SELECT livestream_id,COUNT(*) n FROM reactions GROUP BY livestream_id) r ON r.livestream_id=l.id
LEFT JOIN (SELECT livestream_id,COUNT(*) n,SUM(tip) tip FROM livecomments GROUP BY livestream_id) c ON c.livestream_id=l.id;
INSERT INTO user_stats(user_id,user_name,reaction_count,comment_count,total_tip)
SELECT u.id,CAST(u.name AS BINARY),COALESCE(s.reactions,0),COALESCE(s.comments,0),COALESCE(s.tips,0)
FROM users u LEFT JOIN (
  SELECT l.user_id,SUM(s.reaction_count) reactions,SUM(s.comment_count) comments,SUM(s.total_tip) tips
  FROM livestreams l JOIN stream_stats s ON s.livestream_id=l.id GROUP BY l.user_id
) s ON s.user_id=u.id;

DELIMITER $$
CREATE TRIGGER user_stats_insert AFTER INSERT ON users FOR EACH ROW
BEGIN
  INSERT INTO user_stats(user_id,user_name) VALUES(NEW.id,CAST(NEW.name AS BINARY));
END$$
CREATE TRIGGER stream_stats_insert AFTER INSERT ON livestreams FOR EACH ROW
BEGIN
  INSERT INTO stream_stats(livestream_id) VALUES(NEW.id);
END$$
CREATE TRIGGER livecomment_stats_insert AFTER INSERT ON livecomments FOR EACH ROW
BEGIN
  UPDATE stream_stats SET comment_count=comment_count+1,total_tip=total_tip+NEW.tip WHERE livestream_id=NEW.livestream_id;
  UPDATE user_stats SET comment_count=comment_count+1,total_tip=total_tip+NEW.tip WHERE user_id=(SELECT user_id FROM livestreams WHERE id=NEW.livestream_id);
END$$
CREATE TRIGGER livecomment_stats_delete AFTER DELETE ON livecomments FOR EACH ROW
BEGIN
  UPDATE stream_stats SET comment_count=comment_count-1,total_tip=total_tip-OLD.tip WHERE livestream_id=OLD.livestream_id;
  UPDATE user_stats SET comment_count=comment_count-1,total_tip=total_tip-OLD.tip WHERE user_id=(SELECT user_id FROM livestreams WHERE id=OLD.livestream_id);
END$$
CREATE TRIGGER reaction_stats_insert AFTER INSERT ON reactions FOR EACH ROW
BEGIN
  UPDATE stream_stats SET reaction_count=reaction_count+1 WHERE livestream_id=NEW.livestream_id;
  UPDATE user_stats SET reaction_count=reaction_count+1 WHERE user_id=(SELECT user_id FROM livestreams WHERE id=NEW.livestream_id);
END$$
CREATE TRIGGER reaction_stats_delete AFTER DELETE ON reactions FOR EACH ROW
BEGIN
  UPDATE stream_stats SET reaction_count=reaction_count-1 WHERE livestream_id=OLD.livestream_id;
  UPDATE user_stats SET reaction_count=reaction_count-1 WHERE user_id=(SELECT user_id FROM livestreams WHERE id=OLD.livestream_id);
END$$
DELIMITER ;
