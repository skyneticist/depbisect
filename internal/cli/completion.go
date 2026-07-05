package cli

import (
	"fmt"
	"io"
)

const bashCompletion = `# bash completion for depbisect
# Enable for the current shell or from ~/.bashrc:
#   source <(depbisect completion bash)

# Git refs (branches, tags, remotes) from the repository the command will
# operate on: an earlier --repo <path> on the line wins over the current
# directory. Ref names cannot contain whitespace, so word splitting is safe.
_depbisect_refs() {
    local repo="." i
    for ((i=2; i<COMP_CWORD; i++)); do
        if [ "${COMP_WORDS[i]}" = "--repo" ]; then
            repo="${COMP_WORDS[i+1]}"
        fi
    done
    git -C "$repo" for-each-ref --format='%(refname:short)' \
        refs/heads refs/tags refs/remotes 2>/dev/null
}

_depbisect() {
    local cur prev
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"
    case "${COMP_WORDS[1]}" in
        run)
            case "$prev" in
                --base|--to)
                    COMPREPLY=( $(compgen -W "$(_depbisect_refs)" -- "$cur") )
                    return
                    ;;
                --pm)
                    COMPREPLY=( $(compgen -W "npm pnpm cargo go uv" -- "$cur") )
                    return
                    ;;
                --style)
                    COMPREPLY=( $(compgen -W "modern classic" -- "$cur") )
                    return
                    ;;
            esac
            COMPREPLY=( $(compgen -W "--base --to --repo --runs --jobs -j --run-timeout --install-timeout --overall-timeout --pm --report-md --report-json --no-reports --checkpoint --resume --keep-worktrees --dry-run --quiet --verbose --style --" -- "$cur") )
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh" -- "$cur") )
            ;;
        *)
            COMPREPLY=( $(compgen -W "run version completion help" -- "$cur") )
            ;;
    esac
}
complete -F _depbisect depbisect
`

const zshCompletion = `#compdef depbisect
# zsh completion for depbisect
# Enable from ~/.zshrc (after compinit):
#   source <(depbisect completion zsh)
# or install into your $fpath (before compinit):
#   depbisect completion zsh > "${fpath[1]}/_depbisect"

# Git refs (branches, tags, remotes) from the repository the command will
# operate on: an earlier --repo <path> on the line wins over the current
# directory. Runs against the shifted words array set up by the run branch.
_depbisect_refs() {
    local repo="." i out
    for ((i = 2; i < CURRENT; i++)); do
        if [[ "$words[i]" == "--repo" ]]; then
            repo="$words[i+1]"
        fi
    done
    out="$(command git -C "$repo" for-each-ref --format='%(refname:short)' \
        refs/heads refs/tags refs/remotes 2>/dev/null)"
    [[ -n "$out" ]] || return 1
    local -a refs
    refs=(${(f)out})
    _describe -t refs 'git ref' refs
}

_depbisect() {
    local -a commands
    commands=(
        'run:bisect dependency updates'
        'version:print version'
        'completion:generate shell completion'
        'help:show usage'
    )
    if (( CURRENT == 2 )); then
        _describe 'command' commands
        return
    fi
    case "$words[2]" in
        run)
            # _arguments matches specs against the whole word array; drop the
            # "run" subcommand word so the option specs line up.
            shift words
            (( CURRENT-- ))
            _arguments \
                '--base[base revision]:rev:_depbisect_refs' \
                '--to[target revision]:rev:_depbisect_refs' \
                '--repo[repository path]:path:_directories' \
                '--runs[runs per candidate]:n:' \
                '(-j --jobs)'{-j,--jobs}'[evaluate candidates in parallel, each in its own worktree]:n:' \
                '--run-timeout[timeout per run]:duration:' \
                '--install-timeout[timeout per dependency install]:duration:' \
                '--overall-timeout[timeout for the complete bisection]:duration:' \
                '--pm[package manager]:pm:(npm pnpm cargo go uv)' \
                '--report-md[markdown report path]:path:_files' \
                '--report-json[json report path]:path:_files' \
                '--no-reports[write no reports]' \
                '--checkpoint[resumable checkpoint path]:path:_files' \
                '--resume[resume completed trials from checkpoint]' \
                '--keep-worktrees[keep temporary worktree]' \
                '--dry-run[plan only]' \
                '--quiet[suppress progress and print only the final result]' \
                '--verbose[verbose output]' \
                '--style[output style]:style:(modern classic)'
            ;;
        completion)
            _values 'shell' bash zsh
            ;;
    esac
}

# When installed as a $fpath file, compinit autoloads this file on the first
# completion and the call below runs in completion context. When sourced
# directly instead (source <(...) after compinit), the function must be
# registered with compdef — calling it at the top level would do nothing.
if [ "$funcstack[1]" = "_depbisect" ]; then
    _depbisect "$@"
else
    compdef _depbisect depbisect
fi
`

// completionMain writes the shell completion script for the requested shell to
// stdout. It supports "bash" and "zsh".
func completionMain(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "depbisect: usage: depbisect completion bash|zsh")
		return ExitError
	}
	switch args[0] {
	case "bash":
		fmt.Fprint(stdout, bashCompletion)
		return ExitOK
	case "zsh":
		fmt.Fprint(stdout, zshCompletion)
		return ExitOK
	default:
		fmt.Fprintf(stderr, "depbisect: unsupported shell %q (supported: bash, zsh)\n", args[0])
		return ExitError
	}
}
