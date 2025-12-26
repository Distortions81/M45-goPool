# Miner Submission Flow

```
          +------------------+             +-----------------------+
          | Miner Connection |             |  Job Manager / Job    |
          | (TCP or TLS)     |             |  Notification Feed    |
          +--------+---------+             +-----------+-----------+
                   |                                 |
                   | wait for subscribe/authorize    |
                   |-------------------------------->|
                   |                                 |
                   |      Initial job + extranonce   |
                   |<--------------------------------|
                   |                                 |
            Stratum requests (submit)                |
                   |                                 |
                   v                                 v
          +------------------+         +------------------------------+
          | handleSubmit     |         | listenJobs + sendNotifyFor   |
          +--------+---------+         +------------------------------+
                   |                                 ^
                   |  lightweight syntax checks       |
                   |  workers/job/nonce/version       |
                   |  duplicate detection             |
                   v                                 |
          +------------------+         +------------------------------+
          | submissionTask    |        |  Job payload updated per job |
          | enqueue           |<-------|  + difficulty updates         |
          +--------+---------+         +------------------------------+
                   |
                   v
          +------------------+
          | Worker Pool       |  runtime.NumCPU() goroutines
          +--------+---------+
                   |
                   | invokes
                   v
          +-----------------------+
          | processSubmissionTask |
          +--------+--------------+
                   |
     +-------------+-----------------+
     |             |                 |
     v             v                 v
  header        hashing/cmp           result stats/logs
  build         against target       (recordShare, lowDiff)
     |                              |
     +------------+-----------------+
                  |
        if isBlock |
                  v
         +-------------------------+
         | handleBlockShare        |
         | build full block        |
         | submitblock retry loop  |
         | log pending/found block |
         +------------+------------+
                      |
                      v
                +-----------+
                | writeJSON |
                | (mutex)   |
                +-----------+
```

Backpressure note: the bounded `submissionWorkerPool` queue prevents unbounded goroutine spikes, while fixed workers and `writeMu` serialize responses to the miner.
