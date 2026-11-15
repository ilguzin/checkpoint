ALTER TABLE users ADD COLUMN ui_locale TEXT NOT NULL DEFAULT 'auto' CHECK(ui_locale IN ('auto','en','ru'));
