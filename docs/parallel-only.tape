# docs/parallel-only.tape — VHS script for a standalone "parallel bisection" GIF.
#
# Records ONE bisection on the largest offline example (demo-swarm: twenty-eight
# dependency changes, a twelve-package combination-locked culprit) with
# --jobs 12: twelve worktrees evaluating candidates concurrently. Pair it with
# docs/sequential.tape (the same bisection at --jobs 1) when laying the two runs
# out one after another; docs/parallel.tape combines both into one GIF.
#
#   Generate:  vhs docs/parallel-only.tape
#   Optimize:  shrink the GIF ~25% losslessly with a tight terminal palette:
#                ffmpeg -y -i docs/parallel-only.gif -vf \
#                  'split[s0][s1];[s0]palettegen=max_colors=48:stats_mode=full[p];[s1][p]paletteuse=dither=none' \
#                  /tmp/parallel-only.opt.gif && mv /tmp/parallel-only.opt.gif docs/parallel-only.gif
#
# TypingSpeed, Framerate, and PlaybackSpeed match the other --jobs tapes so the
# three GIFs share one visual cadence and the standalone run lengths stay
# proportional to real time. Tune the Sleep after Enter to cover the real run.

Output docs/parallel-only.gif

Set Shell bash
Set FontSize 16
Set Width 1700
Set Height 800
Set Padding 24
Set Theme "Dracula"
Set TypingSpeed 40ms
Set Framerate 24
Set PlaybackSpeed 1.5

# --- Setup (hidden): build, generate the demo repo, clear any old state ---
Hide
Type "./examples/make-demo-swarm.sh >/dev/null 2>&1 && go build -o depbisect ./cmd/depbisect && rm -f .depbisect-checkpoint.jsonl depbisect-report.* && clear"
Enter
Sleep 10s
Show

# --- Parallel: twelve worktrees evaluate candidates concurrently (--jobs 12). ---
Type "./depbisect run --repo examples/demo-swarm/app --base HEAD~1 --runs 3 --jobs 12 -- node test.js"
Sleep 600ms
Enter
Sleep 8s
