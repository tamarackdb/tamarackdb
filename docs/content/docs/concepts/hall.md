---
title: "The hall"
description: "A picture of the transaction model: people with notebooks, a board, a card cabinet, and a head clerk, to reason about concurrency without technical details."
slug: "hall"
weight: 3
---

A picture helps to reason about the [transaction model](/docs/concepts/transactions/). It follows [Pull-The-Plug
Modeling](https://maximegosselin.com/posts/pull-the-plug-modeling/): imagine the work done with no electricity, by
people with paper and pencils, to reason about concurrency without getting lost in technical details.

- **The hall is the application, and the people are its threads or requests.** The application decides how many
  people come into the hall, not TamarackDB.
- **The board and the card cabinet.** The board is large, on wheels, and it pivots; its name is written at the top of
  the side that faces the hall. On it is the log of events, numbered in the order they're added (`sequence`).
  Pages are glued end to end: whoever reads the board sees one long list of events, not pages. Nothing is ever taken
  off the board. In a large card cabinet are the projections: one drawer per type, and in it one card per projection,
  filed under its id, with a version stamp, new on every write. A card can be replaced or pulled out. No one searches
  the cabinet: a card is found by its drawer and its id. Everyone can look at both, but only the clerks change
  them. People keep their backs to them, and only turn around when they really need to read. No one is told
  when the clerks write.
- **The notebook.** Starting a transaction is taking a blank notebook. A person holds one at a time, and no one else
  sees it. In it, the person writes down their wishes, with no numbers: only events glued to the board have one. When
  they look at the board or into the card cabinet, they add in their head what's in their notebook. They can throw the
  notebook away at any time; handing it to the head clerk is the write. Either way, they no longer have a notebook.
- **A notebook that stands on its own.** The head clerk knows nothing of what the person did before reaching them:
  everything to check is written in the notebook. Next to each wish for events, the person notes what the decision
  rests on: the board's name and the number they read it up to (`afterSequence`), and which events would have changed
  their mind (`failIfEventsMatch`). A wish that rests on no reading notes neither name nor number: it holds on any
  board. For each card they want to change, they note the version they read.
- **The bookmark.** One person can follow several lines of thought in the same transaction (the processors). When they
  look at the board for one of them, they slip a bookmark into the notebook. Before writing down the wish that comes out
  of it, they read again what was added to the notebook after the bookmark. If one of those additions bears on the
  decision, they tear the notebook up: they decided on a stale view, and the whole notebook is suspect. This is the
  only check that falls to them: only they know their notebook in order.
- **A projector is a person like any other.** In an eventually consistent application, the person playing a projector
  wears a watch that reminds them, now and then, to go look at the board: no one tells them when the clerks write.
  They take a notebook (it costs nothing), read the card holding their marker in the card cabinet (the board's name
  and the last number processed) and note its version, then look at the board after that number. If they find new
  events, they write down the cards to file, replace, or pull out, and their new marker, and get in line.
  Otherwise, they throw the blank notebook away. Their notebook never holds new events: the ones they project are
  already on the board. A newcomer with no marker card reads the board from the start: they see its name there, and
  note their first marker with the last number processed. A rebuild goes the same way, in one notebook or several.
- **A refused projector starts over.** If the head clerk refuses a projector's notebook because a card is no longer at
  the version noted, nothing is written, neither the cards nor the new marker, since they go together. The person
  throws the notebook away and starts again at the next tick of the watch, from their marker card: if someone moved
  that marker in the meantime, they pick up where it stopped, with no event projected twice or skipped. A card doesn't
  carry the name of who wrote it: the head clerk can't stop two people from touching the same ones.
- **The head clerk, in a corner of the hall, handles notebooks one at a time.** People who are ready line up in
  front of them. Once in line, a person no longer touches their notebook. They can leave the line; the person behind
  them then takes their place.
- **Only the head clerk decides, and only once.** They check what the notebook notes against the board and the
  card cabinet as they are at that moment. If the board's name isn't the one the notebook notes, if an event added
  after the noted number would have changed the decision, or if a card is no longer at the version noted, they refuse
  the whole notebook (`409`). Otherwise, they have everything written. No one tries to guess the verdict: turning
  around to look at the board just before getting in line would prove nothing, since the board can change during the
  wait. Refused, the person leaves the line. If they want, they start over: take a blank notebook, look at the board,
  decide, write it down.
- **Under-clerks who act together.** The head clerk directs under-clerks: one who glues to the board, after the last
  page, a page holding every event of the notebook, and one for each card touched in the card cabinet (filed,
  replaced, or pulled out). They all wait for the order, then act together. No one ever sees a write half done: the
  whole notebook appears at once, or nothing does (the SQLite transaction).
- **Leaving before or during the write.** The person must still be there when the head clerk takes their notebook;
  if they left before, nothing is written. From then on, the head clerk goes to the end, checks and writing alike, even
  if the person leaves. They then don't know whether their notebook was written, and it's up to them to deal with it.
- **Emptying drawers.** A person with no notebook can ask the head clerk to empty the drawer of one type, or the whole
  card cabinet. They get in line like everyone else: the notebooks that arrived before them are written
  first, and the removal applies to everything accepted before it.
- **Turning the board around (dev mode only).** A person with no notebook can ask for the board to be turned around.
  The head clerk handles the request in its turn, like a notebook: the ones that arrived before it are written on the
  visible side, then the board pivots. The side that comes up is blank and carries a new name, and no one can read the
  old side any more. The card cabinet is emptied at the same time. A notebook that notes the old name will be
  refused. Those who keep a marker elsewhere (a projector that remembers the name and the last number read, for
  example) see that the name changed, and start over from zero.

In the hall, people make writes. Committing is what the SQLite transaction does inside one write.
