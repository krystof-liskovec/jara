# Git commit identity

Git uses a name and email to author commits. Git hosting tokens and SSH keys
authorize clone/push operations independently; configuring those credentials
does not set a commit identity.

Configure a Jara environment from your laptop:

```sh
jara config git-identity set --remote work \
  --name 'Your Name' --email 'you@example.com'
jara config git-identity show --remote work
```

Inside a Jara sandbox or ordinary SSH shell, configure that environment directly:

```sh
jara config git-identity set --local \
  --name 'Your Name' --email 'you@example.com'
jara config git-identity show --local
```

`--local` selects the machine running Jara. The setting is still a user-wide Git
default for that environment, rather than a repository-local setting. Omit it
when configuring a named remote. Local and remote selection cannot be combined.

The setting applies to future Git commands, including those run by already-open
agents. No restart is needed. Repository-specific identities and explicit Git
author/committer environment variables retain their normal precedence.

Jara stores the identity in a managed Git include file in its persistent data
directory and registers it in the user's global Git configuration. It does not
copy your laptop's entire Git configuration, credentials, signing keys, or hooks.
The normal agent home/data volumes preserve the setting across container updates.

Remove only Jara's managed identity to return to any previous global defaults:

```sh
jara config git-identity clear --remote work
# Or, inside the environment:
jara config git-identity clear --local
```

`show` reports global name/email, including Git includes. A repository can
override those values; inspect its effective values with `git config user.name`
and `git config user.email` from that checkout. `jara doctor` reports a setup hint
when the environment has no complete global commit identity.

Upgrade the remote Jara installation before using these commands from a newer
client: `jara deployment upgrade --remote work`.
