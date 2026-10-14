# Triage Labels

The skills speak in terms of five canonical triage roles. This file maps those roles to the actual label strings used in this repo's issue tracker.

| Label in mattpocock/skills | Label in our tracker | Meaning                                  |
| -------------------------- | -------------------- | ---------------------------------------- |
| `needs-triage`             | `needs-triage`       | Maintainer needs to evaluate this issue  |
| `needs-info`               | `needs-info`         | Waiting on reporter for more information |
| `ready-for-agent`          | `ready-for-agent`    | Fully specified, ready for an AFK agent  |
| `ready-for-human`          | `ready-for-human`    | Requires human implementation            |
| `wontfix`                  | `wontfix`            | Will not be actioned                     |

When a skill mentions a role (e.g. "apply the AFK-ready triage label"), use the corresponding label string from this table.

Edit the right-hand column to match whatever vocabulary you actually use.

## Wayfinder labels

`/wayfinder` labels its map and its tickets with exactly these strings — nothing else:

| Label                  | Meaning                                        |
| ---------------------- | ---------------------------------------------- |
| `wayfinder:map`        | The map issue itself (Notes / Decisions / Fog) |
| `wayfinder:research`   | Child ticket: research work                    |
| `wayfinder:prototype`  | Child ticket: throwaway prototype              |
| `wayfinder:grilling`   | Child ticket: grilling / pressure-testing      |
| `wayfinder:task`       | Child ticket: implementation task              |

These ten labels are the whole vocabulary for the skills. No other label is enforced.
