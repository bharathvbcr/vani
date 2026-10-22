# sleep-check

Owner-run check for GitPulse card ft-c9ff73806ffa9c5ed8e80b913c135b31, criterion 3:
a manual `pmset` sleep across a scheduled minute is recorded in the card log.

It runs the real `daemon.Run` with the real clock against a schedule a few minutes
ahead, then reports whether the run fired promptly after the Mac woke. It never
touches the live config, state directory, ledger or network.

## Steps

1. Plug in or not, either is fine. Start it from a terminal:

   ```
   go -C /Users/bharath/Code/apps/Vani run ./build/sleep-check -in 3m
   ```

   It prints the scheduled wall time (HH:MM, local zone) and waits.
2. Before that minute, run `pmset sleepnow` (or close the lid). The Mac must be
   asleep when HH:MM passes; use `-in 5m` if you need longer.
3. Keep it asleep until after HH:MM, then wake it. The program fires the run,
   prints the verdict and exits.
4. Copy the `RESULT:` line, with the `scheduled`, `fired` and `slept` lines
   above it, into the card log (GitPulse card log) or paste it to Claude.

## Reading the verdict

- `PASS`: the Mac slept across the scheduled minute and the run fired within
  30 s (wakeSlice) + 1 min (onTimeSlack) of waking.
- `FAIL`: the daemon skipped the run as missed, never ran it, fired later than
  that after wake, or (awake) fired more than 35 s after the deadline.
- `INCONCLUSIVE`: no sleep was observed across the scheduled minute (the wall
  clock never jumped over the monotonic clock), so nothing was tested. Run it
  again and make sure the Mac is asleep at HH:MM.

Exit status: 0 PASS, 1 FAIL, 3 INCONCLUSIVE, 2 usage error or Ctrl-C.

Sleep is detected by sampling once a second: the part of a wall-clock delta that
the monotonic clock lacks is time asleep. The check restates the daemon's
unexported `wakeSlice` (30 s) and `onTimeSlack` (1 min) in `verdict.go`.
