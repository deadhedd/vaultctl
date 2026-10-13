# OpenBSD vaultctl lab — reusable test environment

**Established:** 2026-10-13  
**Status:** Working, reusable lab; post-reset guest-content recheck outstanding  
**Purpose:** Test vaultctl against realistic Git-backed Obsidian vault relationships on OpenBSD without using the live `daemon` server or real vault data.  
**Maintainer's rule:** Everything in this VM is synthetic and disposable. Keep authoritative code on Ghost and use the lab for integration tests.

## Architecture

```text
Ghost (Ubuntu; ~/src/vaultctl)
  └─ Cross-compile vaultctl for OpenBSD/amd64
       ↓ SCP
Scholomance (RHEL 10.2; qemu:///system)
  ├─ /usr/local/bin/ephemctl
  ├─ profile: vaultctl-lab
  ├─ protected base: vaultctl-lab-base.qcow2
  └─ VM: vaultctl-lab-proof (OpenBSD 7.9)
       └─ /home/admin/vaultctl-lab/
          ├─ Main.git/             canonical bare vault
          ├─ worktree/             server checkout
          ├─ client/               ordinary vault client clone
          ├─ remotes/              synthetic bare project remotes
          │  ├─ strategy.git/
          │  ├─ vaultctl.git/
          │  └─ ephemctl.git/
          ├─ seeds/                writable source clones for test updates
          │  ├─ strategy/
          │  ├─ vaultctl/
          │  └─ ephemctl/
          ├─ server.json           vaultctl server-mode config
          └─ client.json           vaultctl client-mode config
```

All test repositories are local to the OpenBSD guest. The project names are fixtures, **not clones of the user's real GitHub repositories**. No test commands should point to `/home/git/vaults/Main.git` or `/home/obsidian/vaults/Main` on `daemon`.

## Host, profile, and image inventory

| Item | Value |
| --- | --- |
| Hypervisor host | `scholomance` |
| ephemctl binary | `/usr/local/bin/ephemctl` |
| ephemctl config | `/etc/ephemctl/ephemctl.toml` |
| Profile directory | `/etc/ephemctl/profiles` |
| Specialized profile | `/etc/ephemctl/profiles/vaultctl-lab.toml` |
| Reused domain template | `/etc/ephemctl/profiles/openbsd-7.9.xml` |
| Specialized immutable base | `/var/lib/libvirt/images/base/vaultctl-lab-base.qcow2` |
| Managed overlay directory | `/var/lib/libvirt/images/ephemctl` |
| Backup of flattened configured base | `/home/admin/vaultctl-lab-golden.qcow2` |
| Backup checksum | `/home/admin/vaultctl-lab-golden.qcow2.sha256` |
| Current reusable proof VM | `vaultctl-lab-proof` (profile `vaultctl-lab`) |
| Previous build VM | `vaultctl-lab` (profile **`openbsd-7.9`**) |
| Guest login | `admin`, password-based SSH in observed run |
| Guest's inherited hostname | `necropolis` |
| Installed guest tools | OpenBSD 7.9, Git 2.53.0, `~/bin/vaultctl` |

At handoff, `vaultctl-lab-proof` had been reset and restarted successfully; `vaultctl-lab` had been stopped. **Check current state with `ephemctl status` before assuming it is still running or stopped.** The historical proof guest's observed address was `192.168.122.185`; that address is not permanent. The earlier VM was observed at `192.168.122.172`.

The specialized image was made by flattening the stopped earlier VM's managed overlay. `qemu-img info` reported **24 GiB virtual capacity**, **2.47 GiB physical disk consumption**, QCOW2 without a backing file; `qemu-img check` reported no errors. The installed base has owner/group `qemu:qemu`, permission `0444`, SELinux type `virt_content_t` under enforcing SELinux. The profile passed all reported `ephemctl verify` checks. The installed copy's SHA256 matched the golden file's recorded digest.

### Profile configuration

`/etc/ephemctl/profiles/vaultctl-lab.toml`:

```toml
version = 1
name = "vaultctl-lab"
domain_prefix = "ephem-"
base_image = "/var/lib/libvirt/images/base/vaultctl-lab-base.qcow2"
overlay_dir = "/var/lib/libvirt/images/ephemctl"
domain_template = "/etc/ephemctl/profiles/openbsd-7.9.xml"
```

There is no `image_dir` in the observed global configuration. This manually imported golden base is not tracked through ephemctl's `image stage` / `image prepare` inventory. Do not expect `ephemctl image remove` to manage it.

## Routine use on Scholomance

```sh
EP=/usr/local/bin/ephemctl

"$EP" profiles
"$EP" verify vaultctl-lab
"$EP" list
"$EP" status vaultctl-lab-proof
"$EP" start vaultctl-lab-proof
"$EP" ip vaultctl-lab-proof
"$EP" ssh --user admin vaultctl-lab-proof
```

For the non-bootstrap OpenBSD profile, `create`/`start` confirms VM runtime state, not DHCP or authenticated SSH readiness. If `ip` says no unique unexpired lease is available immediately after boot, retry after the guest has finished initializing. Do not assume the historical guest address still applies.

**SSH trust:** The image contains previously generated host keys. The proof guest presented ED25519 fingerprint `SHA256:FvH8r2mUQvQOQyosuCrh5QmL6ku8qV036bHhJ2upYwc`, which SSH recognized under other addresses. This is historical evidence, **not** authorization to accept a network key without checking it. Verify the exact guest's fingerprint through its trusted console before first network trust; keep strict host-key checking on. Avoid running two clones of this base simultaneously until unique guest identities are provisioned. Clones also inherit the hostname and configured passwords; keep the lab on a trusted isolated network and do not copy secrets into the base.

## Test vault fixtures

Inside the OpenBSD guest, all commands below run as `admin`:

```sh
lab="$HOME/vaultctl-lab"

# Vaultctl configurations
cat "$lab/server.json"
cat "$lab/client.json"

# Basic smoke checks
"$HOME/bin/vaultctl" --config "$lab/server.json" doctor
"$HOME/bin/vaultctl" --config "$lab/server.json" status
"$HOME/bin/vaultctl" --config "$lab/client.json" doctor
"$HOME/bin/vaultctl" --config "$lab/client.json" status

# Fixtures
ls "$lab/worktree/Projects"
ls "$lab/remotes"
```

Expected vault files include `Home.md`, `Projects/Example.md`, and `Projects/Hook-Test.md`. The three source remotes each begin with `docs/README.md` and `docs/development.md`. The `client` clone tracks `origin/main` at the local bare vault `Main.git`.

The bare vault has a `post-receive` hook at `Main.git/hooks/post-receive` which updates the server worktree after pushes to `main`. This fixture's hook uses `git --git-dir=... --work-tree=... reset --hard main`: **it discards independent uncommitted changes in the server worktree**. Do not copy this hook into production or expect it to preserve server-local edits.

### Example: simulate a project update

```sh
lab="$HOME/vaultctl-lab"
project="$lab/seeds/strategy"

printf '\nNew synthetic revision.\n' >> "$project/docs/development.md"
git -C "$project" add docs/development.md
git -C "$project" commit -m "Test strategy docs update"
git -C "$project" push
```

This advances a **local fixture** remote. It does not touch GitHub, the user's production vault, or a real project repository. The project submodule feature is not yet implemented or tested; adding submodules is upcoming vaultctl development work, not a baseline capability verified in this runbook.

## Update the vaultctl binary after code changes

Develop on Ghost; the current golden image preserves one working binary, but newly compiled binaries installed into the guest's writable overlay will disappear on reset.

**On Ghost:**

```sh
cd ~/src/vaultctl
mkdir -p bin
GOOS=openbsd GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -o bin/vaultctl-openbsd ./cmd/vaultctl
scp bin/vaultctl-openbsd scholomance:~/vaultctl-openbsd
```

**On Scholomance:**

```sh
EP=/usr/local/bin/ephemctl
guest_ip=$("$EP" ip vaultctl-lab-proof)
scp ~/vaultctl-openbsd "admin@$guest_ip:vaultctl-upload"
"$EP" ssh --user admin vaultctl-lab-proof
```

**Inside the OpenBSD guest:**

```sh
install -m 0755 ~/vaultctl-upload ~/bin/vaultctl
~/bin/vaultctl --help
~/bin/vaultctl --config ~/vaultctl-lab/server.json doctor
~/bin/vaultctl --config ~/vaultctl-lab/client.json doctor
```

A later project-local transfer helper is reasonable, but not required for the lab to work.

## Restore the known-good baseline

**Destructive:** A reset discards *all changes to that VM's writable disk since the golden image*, including test data, changed Git histories, updated vaultctl binaries, and guest config. Save any useful evidence to Ghost first. Never reset the original `vaultctl-lab` expecting these fixtures: that older VM is recorded against profile `openbsd-7.9`, so its reset returns to the *clean OS base* instead.

On Scholomance:

```sh
EP=/usr/local/bin/ephemctl
"$EP" stop vaultctl-lab-proof &&
"$EP" reset vaultctl-lab-proof --yes &&
"$EP" start vaultctl-lab-proof
```

Reset requires a stopped guest and leaves it stopped; the final `start` is an explicit separate action. After restart, reacquire the current IP, verify guest SSH identity, and re-run the guest smoke checks. The operator reported that this sequence returned success on 2026-10-13, **but the contents were not inspected after that final reset**; record a post-reset guest-content check at next use.

When investigating performance, measure the above three operations separately with `/usr/bin/time -p`, on an expendable lab, rather than estimating a single combined duration.

## Create another VM from the configured base

Do this only when no clone of the golden image is running with the same inherited SSH identity:

```sh
EP=/usr/local/bin/ephemctl
"$EP" verify vaultctl-lab
"$EP" list                    # choose a genuinely unused name
"$EP" create --profile vaultctl-lab vaultctl-lab-next
"$EP" status vaultctl-lab-next
"$EP" ip vaultctl-lab-next
```

Use the name you actually selected with subsequent commands. Ephemctl creates a new VM/overlay with the stored baseline. A new VM does **not** require reinstalling Git, vaultctl, fixtures, or hooks. To dispose of that new VM when you are certain its work is expendable, use `ephemctl destroy NAME --yes` on Scholomance; do not delete overlays or domains manually. Always check the exact environment name before destructive commands.

## Preserve and maintain the base

- The configured baseline is the protected QCOW2 at `/var/lib/libvirt/images/base/vaultctl-lab-base.qcow2`. Treat it as **immutable**; never install updated code or mutate a Git repo inside it directly.
- The independent capture and digest are `/home/admin/vaultctl-lab-golden.qcow2` and `.sha256`. Verify with `sha256sum -c /home/admin/vaultctl-lab-golden.qcow2.sha256` when auditing the source backup. The actual digest value was not pasted into the session transcript.
- The profile and baseline image are local Scholomance host configuration, **not** stored in the vaultctl Git repository; this document records how to find and recreate their relationships.
- If a new golden baseline is wanted, quiesce the chosen VM, make an *independent* flattened image, verify it, and create a separately named base/profile. Do not overwrite the current immutable base while an environment may reference it.
- A manual `chcon --reference` was used to match SELinux context. This label is not guaranteed to persist through future relabel/`restorecon` policy changes; verify `ls -lZ` after such changes. A persistent labeling rule is a separate future decision if needed.
- Keep this runbook updated when moving paths, changing the canonical fixture layout, or replacing the base image. The lab should remain small and purpose-built, not evolve into a second production vault server.

## Open follow-ups

1. Re-run `doctor` on both modes and check test files **after the 2026-10-13 reset**.
2. Capture separate wall times for `stop`, `reset`, and `start` if the reported multi-minute sequence repeats; see the paired ephemctl field report.
3. Decide whether to retire the old `vaultctl-lab` VM (which uses the *clean* OpenBSD profile); do not silently remove it.
4. Before running multiple clones, implement a deliberate guest-identity policy (unique SSH host keys, hostname, and any other machine-specific state).
5. Test vaultctl's proposed submodule integration using these synthetic remotes; the current smoke test does not prove submodule or sync behavior.