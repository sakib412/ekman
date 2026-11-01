CREATE TABLE todos (
    id         bigserial   PRIMARY KEY,
    title      text        NOT NULL,
    owner_id   text        NOT NULL,
    done       boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX todos_owner_id ON todos (owner_id);
