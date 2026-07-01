# CPA Usage Keeper - Deployment Guide

## Overview

The Usage Keeper plugin runs inside the CPA process and collects API usage statistics in real time.

## Features

- Real-time data collection via UsagePlugin callback
- Persistent storage with embedded SQLite
- Browser dashboard with summary cards, model breakdowns, event history
- Quotio-compatible endpoint
- API key privacy (SHA-224 hashing, display masking)
- Health monitoring endpoint with storage metrics
- Model pricing management
- Import/Export support

## Configuration

```yaml
plugins:
  configs:
    usage-keeper:
      enabled: true
      priority: 1
      db_path: "./data/usage-keeper.db"
      retention_days: 90
      max_in_memory_events: 1000
      refresh_seconds: 0
      api_key_hash_salt: ""
```

## Endpoints

**Dashboard**: `/v0/resource/plugins/usage-keeper/dashboard`

**Management API** (requires management key):
- `GET /usage` - Quotio-compatible aggregate
- `GET /usage-keeper/summary` - Aggregate usage stats
- `GET /usage-keeper/models` - Per-model breakdown
- `GET /usage-keeper/events` - Paginated event list
- `POST /usage-keeper/cleanup` - Manual cleanup
- `GET /usage-keeper/health` - Plugin health status
- `GET/PUT/DELETE /usage-keeper/prices` - Model pricing
- `GET /usage-keeper/export` - Export usage data
- `POST /usage-keeper/import` - Import usage data

## Model Pricing

Set prices per 1M tokens:
```bash
curl -X PUT .../prices -H "Authorization: Bearer <key>" \
  -d '{"model": "gpt-4", "price": {"prompt": 30, "completion": 60, "cache": 15}}'
```
