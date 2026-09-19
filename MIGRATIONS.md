# Database Migrations

This project includes versioned SQL migrations in `sql/migrations`.

## Tooling choice

Use [golang-migrate](https://github.com/golang-migrate/migrate) for applying migrations.

A migration version table (`schema_migrations`) is created automatically by the migration tool.

## Running migrations

The project includes a `migrate` service in `docker-compose.yml` that connects to the `db` container on the internal Docker network. This works on all platforms (including Windows, where `--network host` is not supported by Docker Desktop).

### Apply all pending migrations

```bash
docker-compose run --rm migrate
```

### Roll back one migration

```bash
MSYS_NO_PATHCONV=1 docker-compose run --rm --entrypoint migrate migrate \
  -path=/migrations \
  -database "postgres://$PGUSER:$PGPASSWORD@db:$PGPORT/$PGDB?sslmode=disable" \
  down 1
```

### Check current version

```bash
MSYS_NO_PATHCONV=1 docker-compose run --rm --entrypoint migrate migrate \
  -path=/migrations \
  -database "postgres://$PGUSER:$PGPASSWORD@db:$PGPORT/$PGDB?sslmode=disable" \
  version
```

### Create a new migration

```bash
docker-compose run --rm --entrypoint /bin/sh migrate -lc \
  'migrate create -ext sql -dir /migrations -seq add_example_column'
```
