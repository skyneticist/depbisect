# docs/sequential.tape — VHS script for a standalone "sequential bisection" GIF.
#
# Records ONE bisection on the largest offline example (demo-swarm: twenty-eight
# dependency changes, a twelve-package combination-locked culprit) with --jobs 1:
# a single worktree evaluating one candidate at a time. Pair it with
# docs/parallel-only.tape (the same bisection at --jobs 12) when laying the two
# runs out one after another; docs/parallel.tape combines both into one GIF.
#
#   Generate:  vhs docs/sequential.tape
#   Optimize:  shrink the GIF ~25% losslessly with a tight terminal palette:
#                ffmpeg -y -i docs/sequential.gif -vf \
#                  'split[s0][s1];[s0]palettegen=max_colors=48:stats_mode=full[p];[s1][p]paletteuse=dither=none' \
#                  /tmp/sequential.opt.gif && mv /tmp/sequential.opt.gif docs/sequential.gif
#
# TypingSpeed, Framerate, and PlaybackSpeed match the other --jobs tapes so the
# three GIFs share one visual cadence and the standalone run lengths stay
# proportional to real time. Tune the Sleep after Enter to cover the real run.

Output docs/sequential.gif

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

# --- Sequential: one worktree, one candidate at a time (--jobs 1). ---
Type "./depbisect run --repo examples/demo-swarm/app --base HEAD~1 --runs 3 --jobs 1 -- node test.js"
Sleep 600ms
Enter
Sleep 17s
