ALTER TABLE todos ADD COLUMN priority text NOT NULL DEFAULT 'normal';
ALTER TABLE todos ADD COLUMN attachment_url text;
