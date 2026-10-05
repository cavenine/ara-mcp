# Sequence authoring recipe

T05 authoring tools prepare **saved plans only**. They do not start sequences or
call equipment endpoints. Ara remains the owner of sequence persistence and
execution.

## Prepare and inspect

1. Call `get_server_context` and `get_rig_context`. Confirm the intended active
   profile, camera, filter wheel, and filter names before authoring. These reads do
   not prove that a saved body will run on the connected rig.
2. Call `list_sequence_templates` and inspect each returned `body`. The in-memory
   built-ins can be placeholder bodies; prefer the packaged `lrgb-dso` template
   whose executable body was exercised against the pinned Ara/OmniSim baseline.
3. Begin the cooperative control phase with `begin_control`. Pass its `control_id`
   and a new stable `intent_id` to every create, update, or template-instantiation
   call. Use a new intent for a different logical edit.

## Validate and save a constructed plan

Use Ara's opaque body unchanged. The smallest supported imaging example is:

```json
{
  "schemaVersion": "openastroara-sequence-v1",
  "$type": "OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer",
  "Name": "M31 light frames",
  "Items": {
    "$values": [
      {
        "$type": "NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer",
        "ExposureTime": 30,
        "ImageType": "LIGHT",
        "Binning": { "X": 1, "Y": 1 },
        "Gain": -1,
        "Offset": -1
      }
    ]
  }
}
```

`ExposureTime` is seconds. `Gain`/`Offset` of `-1` request Ara/camera defaults.
Include any Ara `$type` data and other fields exactly as provided; the adapter
preserves unknown body data.

Call `validate_sequence` with `{ "body": <sequence body> }`. Read both results:

- `valid` and `reason` are Ara's structural validation result. Ara checks object
  shape, schema version, and reachable exposure instruction count; it does not
  check rig/filter/camera readiness or prove an executable run will succeed.
- `executable_supported` and `support_reason` report the adapter's pinned palette
  check. Only `SequentialContainer`, one finite `LoopCondition` per container,
  `SwitchFilter`, `TakeExposure`, and `Annotation` are accepted. A loop is limited
  to 1,000 iterations, container nesting to 64 levels, and expanded work to 1,000
  planned sequence instructions. Exposures
  must be finite and positive; filters need a name and non-negative slot. Actual
  profile filter membership and device-specific exposure/gain/binning limits are
  not established here.

Save only when `valid` and `executable_supported` are both true:

```json
{
  "control_id": "<from begin_control>",
  "intent_id": "<stable ID for this create>",
  "name": "M31 light frames",
  "body": <the exact sequence body>
}
```

`create_sequence` uses that stable intent as Ara's verified `Idempotency-Key`.
The result contains Ara's saved detail and a dispatch receipt. Then call
`get_sequence` with the returned sequence ID and compare its metadata/body for
review. A save is never a start request.

## Instantiate a template

After reviewing a template body from `list_sequence_templates`, call
`instantiate_sequence_template` with `template_name`, `new_sequence_name`, and
optional object `parameters`, plus `control_id`/`intent_id`. Ara substitutes template
tokens and saves the resulting sequence. Instantiation has no verified upstream
idempotency key: if its result is uncertain, reconcile with `list_sequences` before
trying a new intent. Validate the saved body's structural and palette results, then
read it back with `get_sequence`.

`update_sequence` is a partial update: provide one or more of `name`, `description`,
or `body`, along with the sequence UUID and control/intent IDs. Ara returns a conflict
when that sequence has an active run. On uncertain update outcomes, do not retry
automatically; inspect the saved detail first.

## Start and monitor a saved plan (T06)

After the saved body has been reviewed, use the active `control_id` and a new
`intent_id`:

```json
{
  "control_id": "<from begin_control>",
  "intent_id": "<stable ID for this start>",
  "sequence_id": "<saved Ara sequence UUID>"
}
```

`start_sequence` re-reads the saved plan, checks Ara structural validation and the
adapter palette, requires an active profile and connected camera, then checks each
exposure/gain/offset/binning value against the camera's current reported limits. A
sequence using `SwitchFilter` also requires a connected wheel, an available physical
slot, and a profile label matching the instruction's filter name and position.
`ContinueOnError: true` makes `executable_supported` false and is refused by
`create_sequence`, `update_sequence`, and `start_sequence`: the verified Ara build
can emit `instruction_failed` and still finish the run as `completed`.

Ara returns `accepted` while its sequencer runs asynchronously. The operation receipt
is not a run ID or completion signal. Read `get_sequence_state` until Ara reports a
terminal state; a missing state or uncertain start remains unknown, not completed.
An ambiguous start response is not automatically retried. Reusing the same intent
within the control phase replays the recorded outcome without another start.

`pause_sequence`, `resume_sequence`, `stop_sequence`, and `abort_sequence` require the
current `expected_run_id` from Ara state plus a fresh intent ID. Stop and abort are
distinct Ara commands and use the reserved interrupt lane. Resume sends
`recenter: false` and `refocus: false`, so it does not implicitly move the telescope
or focuser. Ending control or disconnecting the MCP client does not stop an accepted
Ara run.

The opt-in `TestLiveAraSequenceStartAndStateWithPinnedOmniSim` integration check
exercises save/start/complete, pause/resume/stop, a second start/abort, and cleanup
against the loopback-only RPi4 simulator profile. Run it as documented in
[development.md](development.md#testing-ara-integration). Its evidence is limited to
the recorded Ara daemon build and simulated camera/filter wheel.

## Verified template and evidence

The authoring example is Ara's pinned
[`lrgb-dso` template](https://github.com/open-astro/openastro-ara/blob/6374eede73383851486e6fb498a3311a3be58d82/OpenAstroAra.Server/templates/lrgb-dso.json).
That body uses nested `SequentialContainer` blocks with finite `LoopCondition`,
`SwitchFilter`, and NINA `TakeExposure` instructions. The same template family ran
against the pinned Ara development daemon and OmniSim; four reduced one-iteration
filter/exposure blocks produced four simulated frames. The observed target/filter
metadata and run frame count disagreed with the frame catalog; see
[the compatibility evidence](api-contracts.md#sequence-event-restart-and-emergency-stop-checks-2026-10-0405).
Do not claim reliable target/filter attribution from that test.
