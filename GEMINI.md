# Project Guidelines & Rules

## Core Principles
1. **Simplicity First (Early Dev Stage)**:
   - Keep changes simple, minimal, and focused directly on the user's requirements.
   - Do not over-engineer, introduce complex abstractions, or add unnecessary configuration layers.
   - Favor straightforward, readable Go idioms over clever or convoluted architectures.

2. **Zero Unnecessary Infrastructure**:
   - Avoid introducing external daemons, servers, or cloud dependencies (e.g., Redis, separate microservices, external databases) unless explicitly requested.
   - Rely on pure Go solutions and local SQLite (`modernc.org/sqlite`) for persistence and querying with zero operational overhead.

3. **Performance & Reliability**:
   - Keep queries fast and lightweight (<10ms).
   - Ensure bot state and metrics queries are safe and do not block trading execution.

4. **Testing & Verification**:
   - Always run `go test ./...` and ensure all tests pass cleanly before committing changes.
   - Add focused unit tests for new logic and metrics calculations.

5. **GitHub Issue Format**:
   - Keep issue descriptions simple, concise, and structured:
     ```markdown
     ### Summary
     1-2 sentences stating the problem or missing capability.

     ### Proposed Change
     2-3 concise bullet points outlining the exact code changes or tests to add.
     ```

