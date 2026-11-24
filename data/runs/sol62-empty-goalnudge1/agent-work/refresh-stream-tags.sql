SET SESSION group_concat_max_len = 1048576;

UPDATE livestreams l
LEFT JOIN (
    SELECT lt.livestream_id,
           CONCAT('[', GROUP_CONCAT(JSON_OBJECT('id', t.id, 'name', t.name)
                                   ORDER BY lt.id SEPARATOR ','), ']') AS tags
    FROM livestream_tags lt
    JOIN tags t ON t.id = lt.tag_id
    GROUP BY lt.livestream_id
) grouped ON grouped.livestream_id = l.id
SET l.tags_json = COALESCE(grouped.tags, '[]');
