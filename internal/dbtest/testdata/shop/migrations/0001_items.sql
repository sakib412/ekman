CREATE TABLE items (
    id    bigserial PRIMARY KEY,
    name  text NOT NULL UNIQUE
);

INSERT INTO items (name) VALUES ('seed');
