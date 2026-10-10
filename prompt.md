# Ziel: Rückgabe-API der Account-Pipeline (009) überarbeiten

Die Account-Pipeline (`internal/account/pipeline/`, Spec `specs/009-account-pipeline.md`)
verbindet den SnowflakeAccount-Controller (020) mit den Modulen (010–018).
Sie soll dem Controller **alles liefern, was er braucht**, damit er selbst keine
Logik über die Module hinweg zusammensetzen muss.

**Ausgangslage:** Die aktuelle Implementierung funktioniert. Die API ist im Kern
nicht falsch, wurde aber mehrfach angepasst und wirkt dadurch unsauber und
unnötig kompliziert. Es geht also um ein **Aufräumen der API**, nicht um neue
Funktionalität. Das beobachtbare Verhalten (Conditions, Events, Status,
Create/Update-Entscheidungen) bleibt gleich, bis auf die unten aufgeführten
kleinen Verhaltensänderungen.

Lies vorher `specs/009-account-pipeline.md`, `specs/020-snowflakeaccount-controller.md`
und die Controller Guidelines in `CLAUDE.md`.

## Was die Pipeline zurückliefern muss

1. **`ResourceExists` und `ResourceUpToDate`** für `managed.ExternalObservation`
   - Mit diesen beiden Werten steuert der Controller, ob er Create, Update oder nichts aufruft.
   - `ResourceUpToDate` ist `false`, wenn
     - die `metadata.generation` neuer ist als `status.observedGeneration`, oder
     - ein Modul in `Observe` Drift meldet (`Drifted()`).
   - Heute berechnet der Controller das selbst:
     `upToDate := cr.Status.GetObservedGeneration() == cr.Generation && obs.InSync`.
     Diese Logik wandert in die Pipeline. Der Controller übernimmt nur noch
     `obs.ExternalObservation()`.
   - Die Module erkennen heute noch keinen echten Drift. Sobald ein Modul
     zurückliest, meldet es `Drifted()`, ohne dass sich die API der Pipeline ändert.

2. **Ready-Zustand mit Begründung**
   - `Ready=True`: Der Standard-Reason (`xpv1.Available()`) reicht.
   - `Ready=False`: Der Nutzer muss erfahren, *warum* die Ressource nicht ready ist.
     Provisionierung kann mehrere Minuten dauern. Die Pipeline liefert deshalb die
     fertige Ready-Condition: entweder Available oder Unavailable mit dem Grund aus
     dem ersten `Pending`. Fehler tragen nichts dazu bei, sie landen nur in Synced.
   - Ready bleibt ein Latch (design.md §7.1): Ist Ready einmal true, bleibt es true.
     Auch den Latch wertet die Pipeline aus. Sie liest dafür beim Start des Laufs den
     gespeicherten Ready-Zustand aus `mc.CR()`.

3. **Status-Felder**
   - Heute `AccountLocator`, später eventuell weitere Felder.
   - Die Module schreiben ihre Status-Felder direkt über `mc.CR().Status`, denn
     `TenantDB` braucht den Locator noch im selben Lauf. Die Pipeline setzt
     `status.observedGeneration`. Der Controller persistiert den Status nur.
     Neue Felder ändern deshalb keine Signatur.

4. **Events**
   - Beim Abarbeiten der Pipeline können Events entstehen, z. B. „Account erstellt“.
     Das sind deutlich weniger als Logs, aber ähnlicher Natur.
   - Die Pipeline sammelt die Events aller Module und gibt sie zurück.
     Das tatsächliche Senden übernimmt der Controller.

5. **Modul-eigene Conditions**
   - Jedes Modul kann optional eine eigene Condition (Type, Status, Reason, Message)
     zurückliefern, z. B. `NetworkPolicyApplied`.
   - Die Pipeline reicht diese Conditions an den Controller weiter.

## Abgrenzung

- **Synced** wird nur über Fehler gesteuert: Jeder Fehler aus der Pipeline wird zu
  `Synced=False`. Das macht das Crossplane-Framework anhand des zurückgegebenen
  Errors (`log.Handle(err)`). Pipeline und Controller setzen Synced nie selbst.
- **Ready** setzt das Framework nicht. Die Pipeline berechnet es einschließlich
  Latch (Punkt 2), und der Controller schreibt es über `report` auf die Ressource.

## Gewünschte Richtung

Heute gibt es mehrere ähnliche Objekte, und Ergebnisse werden an verschiedenen
Stellen berechnet. Stattdessen soll gelten:

- **Die Module bereiten ihr Ergebnis selbst vor.** Jedes Modul liefert ein
  einheitliches Ergebnis: Zustand (Done/Pending/Drifted/Failed), Pending-Grund,
  Fehler, optionale Condition und Events. Status-Felder schreibt es direkt über
  `mc.CR()`. Dafür gibt es *einen* Ergebnistyp
  für alle Module, keine Varianten je Phase oder Verwendungsstelle.
- **Das Pipeline-Ergebnis ist im Wesentlichen die Liste der Modul-Ergebnisse.**
  Die Pipeline führt die Module aus und sammelt deren Ergebnisse ein, ohne selbst
  daraus abgeleitete Werte zu speichern.
- **Helper-Methoden am Pipeline-Ergebnis** durchsuchen die einzelnen
  Modul-Ergebnisse und beantworten die Fragen des Controllers, zum Beispiel:
  - Ist ein Fehler aufgetreten? Welcher ist maßgeblich?
  - Ist die Ressource up to date (Generation + alle Module in sync)?
  - Ist die Ressource ready, und falls nicht, warum?
  - Welche Conditions und Events sind angefallen?
- Jeder abgeleitete Wert wird an **genau einer Stelle** berechnet, nämlich in
  so einer Helper-Methode. Der Controller ruft nur Helper auf und rechnet nichts
  selbst.

## Erwartetes Vorgehen

1. Analysiere die aktuelle API (`Observation`, `Outcome`, `PendingReason` usw.)
   und wie der Controller sie nutzt. Benenne konkret, was unsauber oder
   kompliziert ist: ähnliche Objekte mit überlappender Bedeutung, Ergebnisse,
   die an mehreren Stellen berechnet werden, und Stellen, an denen der Controller
   Pipeline-Ergebnisse selbst zusammensetzen muss.
2. Schlag eine vereinfachte API vor, die die Anforderungen oben abdeckt: Typen,
   Signaturen und ein Vorher/Nachher-Beispiel dafür, wie
   `Observe`/`Create`/`Update` im Controller sie nutzen. Weniger Konzepte sind
   besser als mehr.
3. Erst nach meiner Freigabe: Spec 009 (und falls nötig 020) anpassen, danach den
   Code. Die bestehenden Tests müssen inhaltlich weiter bestehen. Nur ihre
   Anpassung an die neue API ist erlaubt.

---

# API-Vorschlag (Entwurf)

## Befund: Was ist heute unsauber?

1. **Zwei Kanäle aus `Observe`, und `inSync` hat zwei Bedeutungen.**
   `Module.Observe` liefert `(inSync bool, Outcome)`. Beim Account-Modul bedeutet
   `inSync` zugleich `Exists` (`pipeline.go`, `Observe`). Dazu kommt ein Widerspruch
   in `observe.go:34`: Ohne Locator liefert das Modul `false, Outcome{}`. Der
   Nullwert von `Outcome` ist aber `StateDone`. Das Modul meldet also gleichzeitig
   „nicht in sync“ und „fertig“.
2. **Ähnliche Objekte.** Es gibt `Observation` und `Result`, beide mit `Outcomes`,
   und dazu den Wrapper `ModuleOutcome`. `PendingReason()` gibt es zweimal.
   `FirstError()` und `AllDone()` gibt es nur an `Result`.
3. **Der Controller berechnet selbst:**
   - `upToDate` aus Generation und `InSync` (`reconciler.go:233`)
   - den Ready-Latch, doppelt in `Observe` und `apply` (`reconciler.go:229` und `:268`)
   - `renderOutcomes` (`reconciler.go:169`)
   - `updateAccountStatus`: `AccountName` und `AccountURL` (`reconciler.go:185`).
     Das ist Wissen des Account-Moduls und gehört eigentlich nicht in den Controller.
4. **Der Status wird an zwei Orten geschrieben.** Das Modul setzt `Locator` und
   `CreatedAt`, der Controller setzt `Name` und `URL`.
5. **Nur ein Event pro Modul** (`*event.Event`).

## Vorschlag

### `module.go`

```go
const (
	StateDone    State = iota // provisioned and matches the spec
	StatePending              // not yet provisioned; Reason says why (Ready=False)
	StateDrifted              // Observe only: provisioned, but differs from the spec
	StateFailed               // Err is set; user vs system is decided by the error itself
)

// Outcome is everything one module reports from one Observe or Apply call.
type Outcome struct {
	Module    string          // set by the pipeline, never by the module
	State     State
	Reason    string          // Pending: why it is waiting; becomes Ready's message
	Err       error           // Failed: errors.NewUserError(...) or a wrapped system error
	Condition *xpv1.Condition // optional: a condition this module owns
	Events    []event.Event   // optional: zero or more events
}

func Done() Outcome
func Pending(reason string) Outcome
func Drifted() Outcome
func Failed(err error) Outcome

func (o Outcome) WithCondition(c xpv1.Condition) Outcome
func (o Outcome) WithEvent(e event.Event) Outcome

type Module interface {
	Name() string
	// Observe is read-back only. Done: provisioned and matches the spec.
	// Pending: not yet provisioned. Drifted: provisioned, but differs from
	// the spec. Failed: could not read back.
	Observe(ctx context.Context, mc *ModuleContext) Outcome
	Apply(ctx context.Context, mc *ModuleContext) Outcome
	Teardown(ctx context.Context, mc *ModuleContext) error
}
```

Was sich gegenüber heute ändert:

- **Kein `inSync`-Bool mehr, dafür `StateDrifted`.** Ready und UpToDate bleiben
  getrennt: `Pending` wirkt nur auf Ready, `Drifted` nur auf UpToDate. Drift ist ein
  eigener Zustand und kein Bool am Outcome, denn mit einem Konstruktor `Drifted()`
  lässt sich „drifted“ ohnehin nicht mit „pending“ kombinieren. Ein Grund-String ist
  unnötig, weil ihn niemand auswertet. Soll eine Drift sichtbar werden, hängt das
  Modul selbst ein Event an:
  `pipeline.Drifted().WithEvent(event.Normal("DriftDetected", "..."))`. Pipeline
  und Controller erzeugen dafür kein eigenes Event.

  Jeder Helper wertet die Zustände genau einmal aus:

  | Helper | Done | Pending | Drifted | Failed |
  |---|---|---|---|---|
  | `Ready()` / `PendingReason()` | ready | nicht ready, `Reason` | ready | nicht ready, kein Grund (Fehler nur in Synced) |
  | `Observation.upToDate()` (zusätzlich `generationApplied`) | ✓ | ✓ | ✗ | – |
  | `Observation.exists()` (nur Account-Modul) | ✓ | ✗ | ✓ | – |
  | `Result.complete()` | ✓ | ✗ | ✗ | ✗ |
  | Gate stoppt `Apply` | nein | ja | ja | ja |

  Pending in `Observe` löst selbst kein Update aus. Das ist auch nicht nötig: Ein
  `Apply`, das Pending meldet, markiert die Generation nicht als angewendet. Das
  nächste `Observe` sieht deshalb `generationApplied == false` und löst Update aus.
  Fehler wirken nur auf Synced, nie auf Exists oder UpToDate (`–` in der Tabelle).
  Meldet ein Modul aus `Observe` `Failed`, gibt der Controller `log.Handle(obs.Err())`
  zurück. Crossplane setzt dann `Synced=False`, speichert die zuvor gesetzten
  Conditions mit, versucht es mit Backoff erneut und ruft weder `Create` noch `Update`
  auf (crossplane-runtime v2.2.0, `pkg/reconciler/managed/reconciler.go:1118–1135`).
  Exists und UpToDate werden in diesem Fall gar nicht ausgewertet. Fehler in
  `Observe` sind dabei die Ausnahme: Fachliche Prüfungen wie „existiert die Region?“
  gehören in `Apply`.
  `Drifted` aus `Apply` gilt als nicht Done. Die Generation wird dann nicht als
  angewendet markiert, und der nächste Durchlauf versucht es erneut.
- **Kein `ModuleOutcome` mehr.** Die Pipeline trägt den Modulnamen direkt in
  `Outcome.Module` ein.
- **Kein `Rejected` mehr.** Ob ein Fehler vom Nutzer oder vom System kommt, steht
  bereits im Fehler selbst (`errors.NewUserError` / `errors.IsUserError`), und
  `log.Handle` wertet genau das aus. `StateRejected` war eine zweite Quelle für
  dieselbe Information, die nirgends im Produktionscode gelesen wurde. Module
  schreiben `pipeline.Failed(errors.NewUserError(...))`, Tests prüfen
  `errors.IsUserError(outcome.Err)`.
- **Kein `Abort`/`Aborting()` mehr.** Ob ein Modul den Lauf stoppt, hängt nicht
  vom einzelnen Ergebnis ab, sondern ist eine feste Eigenschaft des Moduls: Das
  Account-Modul bricht heute bei jedem Ergebnis außer Done ab, Guardrail- und
  Quota-Check (010/011) werden es genauso tun. Deshalb wird das bei der
  Registrierung festgelegt (siehe `Gate` unten). Die Regel lautet: Ein Gate-Modul,
  das nicht `Done` ist, beendet `Apply`.

### `pipeline.go`

```go
// Outcomes is every module's Outcome from one run, in execution order.
// All derived values are computed here, nowhere else.
type Outcomes []Outcome

func (o Outcomes) Err() error                   // first Failed Err, or nil
func (o Outcomes) AllDone() bool                // every entry is Done
func (o Outcomes) Conditions() []xpv1.Condition // every module-owned condition, in order
func (o Outcomes) Events() []event.Event        // every event, in order
func (o Outcomes) PendingReason() string        // first Pending outcome's Reason, or ""; Failed never contributes (errors go to Synced only)

// Observation is Pipeline.Observe's result.
type Observation struct {
	Outcomes
	generationApplied bool // snapshot at run start: observedGeneration == generation
	readyLatched      bool // snapshot at run start: persisted Ready == True
}

func (o Observation) ExternalObservation() managed.ExternalObservation // {exists(), upToDate()}
func (o Observation) Ready() xpv1.Condition // latched → Available(), else Unavailable(PendingReason)
func (o Observation) exists() bool          // account module's outcome is Done or Drifted
func (o Observation) upToDate() bool        // generationApplied && no outcome is Drifted

// Result is Pipeline.Apply's result.
type Result struct {
	Outcomes
	readyLatched bool // snapshot at run start: persisted Ready == True
}

func (r Result) Ready() xpv1.Condition // latched || complete() → Available(), else Unavailable(PendingReason)
func (r Result) complete() bool        // AllDone(): the generation counts as applied; a stopped run always contains a non-Done Gate

// Report is what the controller renders onto the resource after any run.
type Report interface {
	Events() []event.Event
	Conditions() []xpv1.Condition // module-owned conditions only
	Ready() xpv1.Condition        // the resource's aggregate Ready condition
}

var (
	_ Report = Observation{}
	_ Report = Result{}
)

// Gate marks m as a prerequisite: Apply stops after it unless it is Done.
func Gate(m Module) Module

func New(modules ...Module) *Pipeline // e.g. New(Gate(guardrailcheck), Gate(account), network, auth)

func (p *Pipeline) Observe(ctx context.Context, mc *ModuleContext) Observation
func (p *Pipeline) Apply(ctx context.Context, mc *ModuleContext) Result // sets status.observedGeneration iff complete()
func (p *Pipeline) Destroy(ctx context.Context, mc *ModuleContext) error // unverändert
```

`Observation` und `Result` bleiben bewusst zwei Typen. Die beiden Läufe
unterscheiden sich fachlich:

- Nur `Observe` kennt Exists und UpToDate.
- Nur `Apply` kann abbrechen und eine Generation als angewendet markieren.

Alles, was beide gemeinsam haben, steckt im eingebetteten `Outcomes`. Der
Controller behandelt beide gleich über das Interface `Report`. Ready ist darin
bewusst eine eigene Methode und keine der Modul-Conditions, weil es die
zusammengefasste Condition der Ressource ist.

Alles rund um die Generation liegt in der Pipeline: `Observe` vergleicht sie
(`generationApplied`), und `Apply` setzt am Ende `status.observedGeneration`
über `mc.CR()`, wenn der Lauf vollständig war. Der Controller kennt die
Generation nicht mehr.
`xpv1.Available()` entsteht an genau einer Stelle, in einem privaten Helper, den
beide `Ready()`-Methoden nutzen.

### Status

Das Account-Modul schreibt alle Account-Status-Felder selbst über
`mc.CR().Status`: `Locator`, `CreatedAt`, `Name` und `URL`. `updateAccountStatus`
wandert also aus dem Controller ins Modul. Der Controller persistiert den Status
nur noch.

## Controller vorher/nachher

```go
// Observe
mc := pipeline.NewModuleContext(cr, labels, log, e.pool)
obs := e.pipeline.Observe(ctx, mc)
e.report(cr, obs)
// log.Handle(nil) == nil. On an error Crossplane ignores the observation, sets
// Synced=False, persists the conditions above and calls neither Create nor Update.
return obs.ExternalObservation(), log.Handle(obs.Err())

// apply (Create/Update)
res := e.pipeline.Apply(ctx, mc)
e.report(cr, res)
err := log.Handle(res.Err())
// … Status().Update wie bisher …
return err

func (e *external) report(cr *v1alpha1.SnowflakeAccount, r pipeline.Report) {
	for _, ev := range r.Events() {
		e.record.Event(cr, ev)
	}
	cr.SetConditions(r.Conditions()...)
	cr.SetConditions(r.Ready())
}
```

Danach gibt es im Controller kein `upToDate :=` mehr, keine doppelte
Latch-Abfrage, kein `SetObservedGeneration`, kein `renderOutcomes` mit Schleife
über die Interna und kein `updateAccountStatus`.

## Verhaltensänderungen (bewusst klein)

1. **Rendern auch ohne Account:** `Observe` rendert Conditions und Ready auch, wenn
   der Account noch nicht existiert. Während der Grace-Period zeigt Ready dann schon
   im Observe den Pending-Grund. Heute tut das erst das direkt folgende `Create`.
2. **Account-Modul `Observe` ohne Locator:** Es liefert
   `Pending("account not created yet")` statt `Outcome{}`. `Exists` bleibt dabei
   identisch zu heute.
3. **Pending in `Observe` macht UpToDate nicht mehr false.** Heute ist das der
   Fall, weil `inSync == false` gilt. Das Update kommt trotzdem, und zwar über die
   Generation (siehe Tabelle oben). Heute betrifft das nur das Account-Modul, und
   dort gilt bei Pending ohnehin `Exists == false`.
4. **Ein Fehler in `Observe` landet sofort in Synced.** Heute führt zum Beispiel ein
   Verbindungsfehler des Account-Moduls zu `Exists == false`. Crossplane ruft dann
   `Create` für einen Account auf, der bereits existiert, setzt dabei `Creating()` und
   bricht so den Ready-Latch. Der Fehler wird erst sichtbar, wenn `Apply` ihn erneut
   trifft. Künftig gibt `Observe` den Fehler über `log.Handle` zurück, mit Log-Eintrag
   und Incident-ID, und es gibt weder `Create` noch `Update`.

## Entscheidungen

- **Ready-Latch in der Pipeline:** Angenommen. Spec 009 wird entsprechend
  angepasst, denn sie hält den Latch heute bewusst aus der Pipeline heraus.
- **Status direkt schreiben:** Angenommen. Die Module schreiben über `mc.CR()`, und
  es gibt keinen Status-Patch im `Outcome`.
- **`exists()` während der Grace-Period:** Das aktuelle Verhalten bleibt.
  `exists()` ist true, wenn das Account-Modul `Done` oder `Drifted` meldet. In der
  Grace-Period ist es also false, und Crossplane ruft `Create` auf.

## Beispiel: Identity-Gruppe nachträglich hinzufügen

1. Die Gruppe wird ins CRD eingetragen. Die Generation steigt auf N, `upToDate()`
   wird false, und Crossplane ruft `Update` auf.
2. Das Identity-Modul liefert in `Apply`
   `Pending("waiting for SCIM").WithCondition(IdentitySynced=False, SyncPending)`.
3. Das hat folgende Wirkung:
   - `status.observedGeneration` bleibt auf N-1, Tools zeigen „in progress“.
   - `Synced` ist True. Pending ist kein Fehler, und es gibt kein Warning-Event.
   - `Ready` bleibt True (Latch).
   - `IdentitySynced` ist False mit Reason `SyncPending`.
4. In jedem Poll-Intervall laufen `Update` und `Apply` erneut. Ist die Gruppe da,
   meldet das Modul `Done()` mit `IdentitySynced=True`. `observedGeneration` wird
   dann N.

---

# Kontext für die nächste Session

## Stand

- Der Vorschlag oben ist abgestimmt, es gibt keine offenen Punkte mehr.
- Nächster Schritt: zuerst Spec 009 und 020 anpassen (Specs gehen vor, siehe
  CLAUDE.md), danach den Code. Die bestehenden Tests sollen inhaltlich weiter
  bestehen und nur an die neue API angepasst werden.
- Betroffene Dateien:
  - `internal/account/pipeline/module.go`, `pipeline.go`, `conditions.go`
  - `internal/account/modules/account/observe.go` und `apply.go`
  - `internal/controller/snowflakeaccount/reconciler.go`
  - die zugehörigen Tests

## Was in den Specs geändert werden muss

- **009, Key Concept „Sequential Modules, One Abort Signal“:** Das Abbruch-Signal
  am Outcome wird durch die Gate-Registrierung ersetzt.
- **009, „PendingReason Is the Only Ready-Adjacent Logic Here“:** Der Latch
  wandert in die Pipeline, der Abschnitt muss neu geschrieben werden.
- **009, „Conditions and Events“:** Events werden eine Liste, und Ready ist eine
  eigene Methode an `Report`.
- **020:** Der Controller wird schlanker: `report`, keine Generation, kein
  `updateAccountStatus`, und `Observe` gibt den Fehler zurück.

## Geprüftes Verhalten von crossplane-runtime v2.2.0

Alle Stellen in `pkg/reconciler/managed/reconciler.go`:

- **`Observe` gibt einen Fehler zurück (1118–1135):** Crossplane erzeugt ein
  Warning-Event `CannotObserve`, setzt `Synced=False` und persistiert den Status
  inklusive der vorher gesetzten Conditions. Weder `Create` noch `Update` wird
  aufgerufen, danach folgt ein Requeue mit Backoff.
- **`Create` war erfolgreich (1408):** Crossplane setzt `xpv1.Creating()`. Damit
  wird Ready überschrieben, auch wenn es vorher True war. Erst das nächste
  `Observe` stellt Ready wieder richtig her.
- **`Update` war erfolgreich:** Crossplane erzeugt das Normal-Event „Successfully
  requested update of external resource“, setzt `ReconcileSuccess` (Synced=True)
  und plant den nächsten Lauf nach dem Poll-Intervall.
- **Conditions:** Jede gesetzte Condition bekommt `observedGeneration =
  generation` (`pkg/conditions/manager.go:69`). Synced hängt damit nicht von
  `status.observedGeneration` ab.
- **Status in `apply` sofort persistieren:** `apply` muss `Status().Update` selbst
  aufrufen. `UpdateCriticalAnnotations` überschreibt sonst den Status im
  Speicher, und der Locator ginge nach `Create` verloren.
- **`log.Handle(nil) == nil`:** Deshalb funktioniert
  `return obs.ExternalObservation(), log.Handle(obs.Err())`.

## Fakten aus dem Code

- **Abbruch im Account-Modul:** Jedes Ergebnis außer `Done` aus `Apply` ist mit
  `.Aborting()` versehen (`apply.go`). Das ist die Grundlage für das Gate.
- **`StateRejected`:** Wird im Produktionscode nirgends gelesen, nur in Tests.
- **Account-Modul `Observe` ohne Locator:** Liefert heute `false, Outcome{}`.
  Der Nullwert ist aber `StateDone`, das ist ein Widerspruch. Künftig liefert
  es `Pending(...)`.

## Fachliche Regeln (design.md §4.3, §7.1)

- **Ready vor der ersten Provisionierung:** Ready bleibt False, bis ein Lauf
  vollständig `Done` war. Danach bleibt Ready True (Latch).
- **Identity-Sync wartet:** Während des Wartens gilt `SyncPending` ohne
  Warning-Event. `SyncTimeout` kommt mit Warning-Event, ist aber kein Abbruch.
  Das Warten zeigt nur `IdentitySynced`, nicht Ready.

## Designprinzipien aus der Abstimmung

- So wenige Konzepte wie möglich. Kein Feld und kein String, den niemand
  auswertet.
- **Pending** wirkt nur auf Ready und liefert den Grund. **Drifted** wirkt nur
  auf UpToDate. **Fehler** wirken nur auf Synced.
- Jeder abgeleitete Wert wird an genau einer Stelle berechnet, in der Pipeline.
  Der Controller ruft nur noch auf.
- Module erzeugen ihre Events selbst (`WithEvent`). Pipeline und Controller
  erzeugen keine eigenen Events.
- Fachliche Prüfungen, zum Beispiel ob eine Region existiert, gehören in
  `Apply`. Fehler in `Observe` sind die Ausnahme.
