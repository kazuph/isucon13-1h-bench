SELECT 'stream_stats_mismatches',COUNT(*)
FROM livestreams l LEFT JOIN stream_stats s ON s.livestream_id=l.id
LEFT JOIN (SELECT livestream_id,COUNT(*) n FROM reactions GROUP BY livestream_id) r ON r.livestream_id=l.id
LEFT JOIN (SELECT livestream_id,COUNT(*) n,SUM(tip) tip FROM livecomments GROUP BY livestream_id) c ON c.livestream_id=l.id
WHERE s.livestream_id IS NULL OR s.reaction_count<>COALESCE(r.n,0) OR s.comment_count<>COALESCE(c.n,0) OR s.total_tip<>COALESCE(c.tip,0);
SELECT 'user_stats_mismatches',COUNT(*)
FROM users u LEFT JOIN user_stats s ON s.user_id=u.id
LEFT JOIN (SELECT l.user_id,SUM(st.reaction_count) r,SUM(st.comment_count) c,SUM(st.total_tip) tip FROM livestreams l JOIN stream_stats st ON st.livestream_id=l.id GROUP BY l.user_id) x ON x.user_id=u.id
WHERE s.user_id IS NULL OR s.user_name<>CAST(u.name AS BINARY) OR s.reaction_count<>COALESCE(x.r,0) OR s.comment_count<>COALESCE(x.c,0) OR s.total_tip<>COALESCE(x.tip,0);
SELECT 'orphan_stream_stats',COUNT(*) FROM stream_stats s LEFT JOIN livestreams l ON l.id=s.livestream_id WHERE l.id IS NULL;
SELECT 'orphan_user_stats',COUNT(*) FROM user_stats s LEFT JOIN users u ON u.id=s.user_id WHERE u.id IS NULL;
