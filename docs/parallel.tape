# docs/parallel.tape — VHS script for the README "--jobs speedup" GIF.
#
# Records the SAME bisection twice on the largest offline example (demo-swarm:
# twenty-eight dependency changes, a twelve-package combination-locked culprit):
# first sequential (--jobs 1), then parallel (--jobs 12). Same minimal set, far
# less wall time. No network required.
#
# The README embeds the separate docs/sequential.gif + docs/parallel-only.gif
# clips instead; this combined GIF is archived under dev_archive/ (gitignored,
# kept for reference and social/launch use). The tape stays in docs/ so the
# combined clip remains reproducible.
#
#   Install VHS:  brew install vhs        (or: go install github.com/charmbracelet/vhs@latest)
#   Generate:     vhs docs/parallel.tape
#   Optimize:     two verbose runs make a large GIF; shrink it ~25% losslessly
#                 with a tight terminal palette (ffmpeg, already a VHS dependency):
#                   ffmpeg -y -i dev_archive/parallel.gif -vf \
#                     'split[s0][s1];[s0]palettegen=max_colors=48:stats_mode=full[p];[s1][p]paletteuse=dither=none' \
#                     /tmp/parallel.opt.gif && mv /tmp/parallel.opt.gif dev_archive/parallel.gif
#
# PlaybackSpeed compresses the GIF's playback uniformly across BOTH runs, so the
# real ~3x ratio is preserved; the on-screen `Duration` lines remain the true
# measured times. Tune the Sleep after each Enter to cover the real run on your
# machine (sequential is the long one).

Output dev_archive/parallel.gif

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

# --- Run 1: sequential. One worktree, one candidate at a time. ---
Type "./depbisect run --repo examples/demo-swarm/app --base HEAD~1 --runs 3 --jobs 1 -- node test.js"
Sleep 600ms
Enter
Sleep 17s

# --- Run 2: parallel. Twelve worktrees evaluate candidates concurrently. ---
# Identical command but for the job count; identical twelve-package result.
Type "./depbisect run --repo examples/demo-swarm/app --base HEAD~1 --runs 3 --jobs 12 -- node test.js"
Sleep 600ms
Enter
Sleep 8s
