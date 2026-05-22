package main

import (
	"fmt"
	"strings"
)

func cmdCompletion(args []string) {
	shell := "zsh"
	if len(args) > 0 {
		shell = args[0]
	}
	switch shell {
	case "zsh":
		fmt.Print(zshCompletion)
	default:
		fatal("Unsupported shell: %s (supported: zsh)", shell)
	}
}

func cmdListWorkers() {
	r, err := newRepoContext()
	if err != nil {
		return
	}
	cfg, err := loadPoolConfig(r.poolConfigFile())
	if err != nil {
		return
	}
	for _, w := range cfg.Workers {
		fmt.Println(displayWorker(w))
	}
}

func cmdListWorkspaces() {
	r, err := newRepoContext()
	if err != nil {
		return
	}
	entries, err := cacheGet(r)
	if err != nil {
		return
	}
	for _, e := range entries {
		fmt.Println(e.Name)
	}
}

func cmdListPoolWorkerStates(state string) {
	r, err := newRepoContext()
	if err != nil {
		return
	}
	cfg, err := loadPoolConfig(r.poolConfigFile())
	if err != nil {
		return
	}
	for _, w := range cfg.Workers {
		ws, err := loadWorkerState(r.workerStateFile(w))
		if err != nil {
			continue
		}
		if state == "" || ws.Status == state {
			desc := ws.Status
			if ws.Ticket != nil {
				desc += " " + *ws.Ticket
			}
			fmt.Printf("%s\t%s\n", displayWorker(w), desc)
		}
	}
}

func cmdInternalComplete(args []string) {
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "workers":
		cmdListWorkers()
	case "workspaces":
		cmdListWorkspaces()
	case "idle-workers":
		cmdListPoolWorkerStates("idle")
	case "done-workers":
		cmdListPoolWorkerStates("done")
	case "failed-workers":
		cmdListPoolWorkerStates("failed")
	case "non-busy-workers":
		r, err := newRepoContext()
		if err != nil {
			return
		}
		cfg, err := loadPoolConfig(r.poolConfigFile())
		if err != nil {
			return
		}
		for _, w := range cfg.Workers {
			ws, err := loadWorkerState(r.workerStateFile(w))
			if err != nil {
				continue
			}
			if ws.Status != "busy" {
				desc := ws.Status
				if ws.Ticket != nil {
					desc += " " + *ws.Ticket
				}
				fmt.Printf("%s\t%s\n", displayWorker(w), desc)
			}
		}
	}
}

var zshCompletion = strings.TrimLeft(`
#compdef wsg

__wsg_workers() {
  local -a workers
  workers=("${(@f)$(wsg __complete workers 2>/dev/null)}")
  _describe 'worker' workers
}

__wsg_non_busy_workers() {
  local -a workers
  workers=("${(@f)$(wsg __complete non-busy-workers 2>/dev/null)}")
  _describe 'worker' workers
}

__wsg_workspaces() {
  local -a workspaces
  workspaces=("${(@f)$(wsg __complete workspaces 2>/dev/null)}")
  _describe 'workspace' workspaces
}

_wsg() {
  local -a commands
  commands=(
    'add:Create workspace'
    'rm:Remove workspace'
    'list:List workspaces'
    'clean:Remove all non-default workspaces'
    'root:Print repo root'
    'where:Show repo and workspace paths'
    'path:Print workspace path'
    'refresh:Rebuild workspace cache'
    'pool:Manage worker pool'
    'dispatch:Dispatch ticket(s) to workers'
    'send:Resume worker with a follow-up prompt'
    'review:Address PR review comments'
    'mount:Open worker in kitty'
    'reset:Reset a worker to idle'
    'status:Show pool status'
    'logs:Tail worker log'
    'completion:Print shell completion script'
    'help:Show help'
  )

  local -a pool_commands
  pool_commands=(
    'list:Show pool status'
    'create:Create or resize pool'
    'resize:Resize pool'
    'rm:Remove a worker from the pool'
    'reset:Reset a worker to idle'
    'destroy:Tear down pool'
  )

  _arguments -C \
    '1:command:->command' \
    '*::arg:->args'

  case $state in
    command)
      _describe 'command' commands
      ;;
    args)
      case $words[1] in
        add|a)
          _arguments \
            '-r[revision]:revision:' \
            '--revision[revision]:revision:'
          ;;
        rm|remove)
          _arguments \
            '--force[force removal]' \
            '1:workspace:__wsg_workspaces'
          ;;
        path)
          _arguments '1:workspace:__wsg_workspaces'
          ;;
        pool)
          _arguments -C \
            '1:pool command:->pool_command' \
            '*::pool arg:->pool_args'
          case $state in
            pool_command)
              _describe 'pool command' pool_commands
              ;;
            pool_args)
              case $words[1] in
                rm|remove)
                  _arguments '1:worker:__wsg_non_busy_workers'
                  ;;
                reset)
                  _arguments '1:worker:__wsg_workers'
                  ;;
                create|resize|r)
                  _arguments '1:size:'
                  ;;
              esac
              ;;
          esac
          ;;
        dispatch|d)
          _arguments \
            '--fg[run in foreground]' \
            '--all[dispatch all ready tickets]' \
            '--no-orchestrate[skip parent issue detection]' \
            '--model[model]:model:(sonnet opus haiku)' \
            '--budget[max spend]:budget:' \
            '--label[label filter]:label:' \
            '*:ticket:'
          ;;
        send|s)
          _arguments \
            '--fg[run in foreground]' \
            '--budget[max spend]:budget:' \
            '1:worker:__wsg_non_busy_workers' \
            '2:prompt:'
          ;;
        review|rev)
          _arguments \
            '--fg[run in foreground]' \
            '--budget[max spend]:budget:' \
            '1:worker:__wsg_non_busy_workers'
          ;;
        mount|m)
          _arguments '1:worker:__wsg_workers'
          ;;
        reset)
          _arguments '1:worker:__wsg_workers'
          ;;
        logs|log)
          _arguments '1:worker:__wsg_workers'
          ;;
        completion)
          _arguments '1:shell:(zsh)'
          ;;
      esac
      ;;
  esac
}

_wsg "$@"
`, "\n")
