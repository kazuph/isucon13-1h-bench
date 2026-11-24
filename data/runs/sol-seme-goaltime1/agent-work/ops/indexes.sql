ALTER TABLE icons ADD INDEX icons_user_id(user_id);
ALTER TABLE themes ADD INDEX themes_user_id(user_id);
ALTER TABLE reservation_slots ADD INDEX slots_start_end(start_at,end_at);
ALTER TABLE livecomments ADD INDEX comments_stream(livestream_id);
ALTER TABLE ng_words ADD INDEX words_stream_user(livestream_id,user_id);
