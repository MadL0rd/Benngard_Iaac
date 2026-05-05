# PostgreSQL Migration Plan: Neon → Exoscale

## 1. Setup users and databases
Connect to `defaultdb` and run:

```sql
-- Users
CREATE USER app_prod WITH PASSWORD 'xxx';
CREATE USER app_dev WITH PASSWORD 'xxx';
CREATE USER app_replit WITH PASSWORD 'xxx';
CREATE USER team_readonly WITH PASSWORD 'xxx';

-- Databases
CREATE DATABASE benngard_prod;
CREATE DATABASE benngard_dev;
```

Switch to `benngard_prod` (`\c benngard_prod`) and run:

```sql
-- app_prod: CRUD + migrations
GRANT CONNECT ON DATABASE benngard_prod TO app_prod;
GRANT USAGE, CREATE ON SCHEMA public TO app_prod;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_prod;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_prod;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_prod;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO app_prod;

-- app_replit: CRUD only (temporary, remove after migration)
GRANT CONNECT ON DATABASE benngard_prod TO app_replit;
GRANT USAGE ON SCHEMA public TO app_replit;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_replit;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_replit;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_replit;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO app_replit;

-- team_readonly: read-only
GRANT CONNECT ON DATABASE benngard_prod TO team_readonly;
GRANT USAGE ON SCHEMA public TO team_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO team_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT ON TABLES TO team_readonly;

-- Fix default privileges for tables created by app_prod
ALTER DEFAULT PRIVILEGES FOR USER app_prod IN SCHEMA public
  GRANT SELECT ON TABLES TO team_readonly;
ALTER DEFAULT PRIVILEGES FOR USER app_prod IN SCHEMA public
  GRANT SELECT ON SEQUENCES TO team_readonly;
```

Switch to `benngard_dev` (`\c benngard_dev`) and run:

```sql
-- app_dev: CRUD + migrations
GRANT CONNECT ON DATABASE benngard_dev TO app_dev;
GRANT USAGE, CREATE ON SCHEMA public TO app_dev;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_dev;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_dev;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_dev;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO app_dev;
```

## 2. Switch Replit to read-only
Connect to Neon and run:

```sql
CREATE USER neon_readonly WITH PASSWORD 'xxx';
GRANT CONNECT ON DATABASE neondb TO neon_readonly;
GRANT USAGE ON SCHEMA public TO neon_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO neon_readonly;
```

Update connection string on Replit to `neon_readonly` and redeploy.

## 3. Backup & restore
```bash
pg_dump "postgresql://...neon.tech/neondb?sslmode=require" -Fc > backup_final.dump

pg_restore --no-owner --no-privileges \
  -d "postgresql://...exo.io/benngard_prod?sslmode=require" backup_final.dump
```

## 4. Post-restore grants
Connect to `benngard_prod` on Exoscale and run:

```sql
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_prod;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_prod;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_replit;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO app_replit;

GRANT SELECT ON ALL TABLES IN SCHEMA public TO team_readonly;
GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO team_readonly;
```

## 5. Switch connection string
Update connection string on Replit from Neon to Exoscale (`app_replit` user) and redeploy.

## 6. Cleanup
Connect to Exoscale `defaultdb` and run:
```sql
DROP USER app_replit;
```

Connect to Neon and run:
```sql
DROP USER neon_readonly;
```