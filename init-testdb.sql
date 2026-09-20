-- Creates a separate database used by the concurrency integration tests, so
-- running them never touches the demo data in the main `cashback` database.
-- Run tests with:
--   TEST_DATABASE_URL='postgres://cashback:cashback@localhost:5432/cashback_test?sslmode=disable' \
--     make -C flash-cashback-service test-integration
CREATE DATABASE cashback_test OWNER cashback;
