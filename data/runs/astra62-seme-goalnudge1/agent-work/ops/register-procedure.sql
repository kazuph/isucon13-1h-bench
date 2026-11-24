DROP PROCEDURE IF EXISTS register_account;
DELIMITER //
CREATE PROCEDURE register_account(
    IN user_name VARCHAR(255), IN public_name VARCHAR(255), IN biography TEXT,
    IN password_hash VARCHAR(255), IN dark BOOLEAN
)
BEGIN
    DECLARE new_user BIGINT;
    DECLARE new_theme BIGINT;
    DECLARE EXIT HANDLER FOR SQLEXCEPTION BEGIN ROLLBACK; RESIGNAL; END;
    START TRANSACTION;
    INSERT INTO users(name,display_name,description,password)
        VALUES(user_name,public_name,biography,password_hash);
    SET new_user=LAST_INSERT_ID();
    INSERT INTO themes(user_id,dark_mode) VALUES(new_user,dark);
    SET new_theme=LAST_INSERT_ID();
    INSERT INTO registered_domains(user_id,name) VALUES(new_user,user_name);
    COMMIT;
    SELECT new_user AS id,new_theme AS theme_id;
END//
DELIMITER ;
