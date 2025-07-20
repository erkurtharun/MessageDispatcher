# MessageDispatcher (Insider Assessment Project)

A Go-based automatic message dispatching system that periodically fetches unsent messages from PostgreSQL (2 messages every 2 minutes by default), sends them via a webhook, marks them as sent, and caches results in Redis (bonus feature). Supports dynamic configuration via Etcd for runtime adjustments without restarts.

---

## Features

- **Automatic Message Sending:** Fetches and sends unsent messages at configurable intervals.
- **API Endpoints:**
    - `POST /start`: Starts the scheduler.
    - `POST /stop`: Stops the scheduler.
    - `GET /sent`: Retrieves a list of sent messages.
    - `GET /swagger/index.html`: Swagger UI for API documentation.
    - `GET /health`: Health check endpoint.
- **Dynamic Configuration:** Runtime settings (e.g., interval, rate limit, auto-sending toggle, webhook URL) via Etcd or YAML fallback.
- **Redis Integration:** Caches sent message IDs/timestamps and scheduler state for persistence across restarts.
- **Custom Scheduler:** Native Go implementation (no cron); handles queuing, backlogs, and fixed-rate execution.
- **Logging & Monitoring:** Structured Zap logging with ECS format, correlation IDs, and production optimizations.
- **Database Migrations:** Automatic via Goose in dev/local environments.
- **Graceful Shutdown:** Handles signals for clean stops.

---

## Quick Start

### 1. Clone the Repository

```bash
git clone <repo-url>
cd MessageDispatcher
```

### 2. Set Up Environment Variables

Create a `.env` file in the root directory (edit with your values, especially secrets):

```env
APP_ENV=local
APP_DB_URL=postgres://postgres:postgres@db:5432/postgres?sslmode=disable
APP_REDIS_ADDR=redis:6379
APP_REDIS_PASSWORD=
APP_REDIS_DB=0
APP_PORT=:8080
APP_REMOTE_ETCD_ENDPOINTS=["etcd:2379"]
APP_SERVER_READ_TIMEOUT=10
APP_SERVER_WRITE_TIMEOUT=30
APP_SERVER_IDLE_TIMEOUT=60
LOG_LEVEL=info
LOG_ENV=production
SERVICE_NAME=message-dispatcher
GIN_MODE=release
USE_HTTPS=false
```

Load the `.env` file: `export $(cat .env | xargs)` (or use a tool like `dotenv`).

### 3. Run with Docker Compose

The easiest way to get everything running: Use Docker Compose to spin up PostgreSQL, Redis, Etcd, and the application in one command. No manual setup required—all dependencies are containerized and interconnected.

```bash
docker-compose up --build -d
```

- This builds and starts the app at `http://localhost:8080`.
- Swagger docs: `http://localhost:8080/swagger/index.html`.
- Logs and services are accessible via Docker (e.g., `docker logs <container>`).
- Stop: `docker-compose down`.

If you need to rebuild: `docker-compose up --build`.

---

## How the Scheduler Works

- **Fixed-Rate Execution:** Enqueues a job every interval (default: 2 minutes) from the last attempt time.
- **Queue Handling:** Processes jobs sequentially. If a job overruns (e.g., >2 minutes), new jobs queue up—no drift or skips.
- **Backlog & Recovery:** On restart, loads last attempt time from Redis to resume exactly (queues missed jobs if needed).
- **Start/Stop:** Controlled via API; auto-starts if `auto_sending` is true in config.

Jobs run reliably in order, with persistence ensuring no loss on crashes/restarts.

---

## API Overview

All endpoints are under `http://localhost:8080` (configurable via `APP_PORT`).

- **Start Scheduler:** `POST /start` (response: "Started")
- **Stop Scheduler:** `POST /stop` (response: "Stopped")
- **List Sent Messages:** `GET /sent` (returns JSON array of messages)
- **Health Check:** `GET /health` (returns {"status": "healthy"})
- **Swagger Docs:** `GET /swagger/index.html` (interactive API explorer)

Use tools like curl or Postman for testing.

---

## Dynamic Configuration

Runtime settings are dynamically loaded from Etcd (watched for changes) or fallback to `configs/dynamic_config.yaml`. Updates apply instantly without restarts.

Set via Etcd CLI:

```bash
etcdctl put /app/config/auto_sending true
etcdctl put /app/config/rate_limit 2
etcdctl put /app/config/scheduler_interval 120  # seconds
etcdctl put /app/config/scheduler_queue_capacity 100
etcdctl put /app/config/webhook_url https://webhook.site/your-url
etcdctl put /app/config/webhook_auth_key your-key
```

Fallback YAML example (`configs/dynamic_config.yaml`):

```yaml
auto_sending: true
rate_limit: 2
scheduler_interval: 120
webhook_url: https://webhook.site/your-url
```

Static config (DB, Redis, etc.) remains in YAML/env vars.

---

## Database Schema

Messages table (auto-migrated; see `migrations/000001_create_messages_table.sql`):

```sql
CREATE TABLE messages (
    id SERIAL PRIMARY KEY,
    recipient_phone TEXT NOT NULL CHECK (recipient_phone != ''),
    content TEXT NOT NULL CHECK (length(content) > 0 AND length(content) <= 160),
    sent_status BOOLEAN NOT NULL DEFAULT FALSE,
    sent_at TIMESTAMP
);
CREATE INDEX idx_messages_unsent ON messages (id) WHERE sent_status = FALSE;
```

Insert test data manually via SQL for testing.

---

## Project Structure

- `main.go`: Entry point (config loading, DB/Redis/Etcd init, Gin router, scheduler start).
- `internal/app/sender`: Service layer for message sending and retrieval.
- `internal/domain`: Entities (Message with validation) and repository interfaces.
- `internal/infrastructure`: DB (PostgreSQL), cache (Redis), HTTP handlers, scheduler, middleware.
- `internal/pkg`: Config loaders (static/dynamic), logger, migrations.
- `configs/`: YAML files for env-specific settings.
- `migrations/`: Embedded SQL for Goose.
- `docs/`: Swagger annotations.

**Tech Stack:** Go, Gin, PostgreSQL (sql.DB), Redis (go-redis), Etcd (clientv3), Zap (logging), Goose (migrations), Swagger (swaggo).

---

## Running Manually (Without Docker)

For development:

1. Install dependencies: Go 1.20+, PostgreSQL 14+, Redis 7+, Etcd.
2. Start services: Run PostgreSQL/Redis/Etcd locally (or use Docker for them separately).
3. Install Go modules: `go mod tidy`.
4. Run: `go run main.go`.
    - Migrations auto-run in `local`/`dev` env.
    - Scheduler starts if enabled.

---

## FAQ / Troubleshooting

- **Scheduler Not Starting?** Verify `auto_sending` in Etcd/YAML; check logs for errors.
- **Webhook Failures?** Confirm URL and auth key; test with curl.
- **Connection Issues?** Check env vars (e.g., DB_URL); retries are built-in with backoff.
- **Logs Too Verbose?** Set `LOG_LEVEL=info` or higher.
- **Custom Interval?** Update via Etcd for live changes.
- **Docker Issues?** Ensure ports are free; rebuild with `--build`.

View logs: `docker-compose logs -f app` or app console output.

---

## Tips

- **Persistence:** Redis ensures scheduler resumes post-restart without missing beats.
- **Scalability:** Rate limits and queue capacity are configurable; add workers if needed.
- **Testing:** Use Swagger for API; insert unsent messages in DB to simulate.
- **Security:** Use HTTPS in prod (set `USE_HTTPS=true` with certs); secure Etcd.
- **Monitoring:** Logs are ELK-ready; add Prometheus if expanding.

For contributions or issues, open a PR. Questions? Feel free to ask!