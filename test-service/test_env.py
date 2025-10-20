#!/usr/bin/env python
import os
import time
import sys

print("=== Environment Variables Test ===")
print(f"Current working directory: {os.getcwd()}")
print("\n--- .env variables (should be loaded) ---")

env_vars_to_check = [
    "DATABASE_URL",
    "API_KEY",
    "DEBUG",
    "PORT",
    "ENABLE_CACHE",
    "CACHE_TTL",
    "OVERRIDE_TEST"
]

for var in env_vars_to_check:
    value = os.environ.get(var, "<not set>")
    print(f"{var}: {value}")

print("\n--- Running continuously, press Ctrl+C to stop ---")
sys.stdout.flush()

while True:
    time.sleep(10)
    print(".", end="")
    sys.stdout.flush()