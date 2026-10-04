---
title: "Mental model"
description: "A picture of the whole model: a board, a card cabinet, employees with notebooks, and a head clerk, to reason about concurrency without technical details."
slug: "mental-model"
weight: 2
---

A picture of the whole model, to read before the details. It follows [Pull-The-Plug
Modeling](https://maximegosselin.com/posts/pull-the-plug-modeling/): imagine the work done with no electricity, by
people with paper and pencils in a large hall, to reason about concurrency without getting lost in technical details.

- **The hall and the counter.** In the hall are the board, the card cabinet, the employees, and the head clerk. The
  application's people stand outside, at the counter. They never touch the board or the cabinet: they give their
  instructions to an employee. The application decides how many of its people come to the counter, not the employees.
- **The board and the card cabinet.** The board is large and on wheels, and the clerks can turn it around; its name
  is written at the top of the side that faces the hall. On it is the log of events, numbered in the order they're
  added. Pages are glued end to end: whoever reads the board sees one long list of events, not pages. Nothing is ever
  taken off the board. In a large card cabinet are the projections: one drawer per type, and in it one card per
  projection, filed under its id, with a stamp, new on every write. A card can be replaced or pulled out. No one
  searches the cabinet: a card is found by its drawer and its id. Only the clerks change the board and the cabinet. No
  one is told when they do.
- **One employee per transaction.** When a person at the counter starts a transaction, an employee takes a blank
  notebook and gives them a number: the employee's. At the top of the notebook, the employee copies the name written
  on the board.
- **The notebook belongs to the employee.** The person at the counter never sees what's in it, nor how it's kept: the
  numbers read, the stamps of the cards, what each decision rests on. They only get answers, and the number.
- **The employee reads for the person.** Asked what the board says about something, the employee looks at the board,
  adds in their head what their notebook already holds, notes the last number on the board, and reports it all.
  Events in the notebook have no number: only events glued to the board have one.
- **A question, then its answer.** After reporting what the board says, the employee waits for the person's decision:
  the events to note, or nothing. They note it next to what it rests on: what they read, and the number they read it
  up to. Only then do they take another instruction. A decision to do nothing is noted too.
- **The cards.** Between two decisions, the person can ask about a card. The first time, the employee looks in the
  cabinet, and notes the card's stamp, or that there is no card. After that, they answer from their notebook. To
  change a card, the person says what it becomes, or that it goes away. The employee notes it, and touches nothing in
  the cabinet.
- **The employee refuses what breaks their employer's rules**: noting events without a reading first, reading again
  before the decision, looking at or changing a card during a decision, changing a card they never looked at, an
  instruction about cards that changes none. A refusal ends their work: they throw the notebook away.
- **The head clerk, in a corner of the hall, handles notebooks one at a time.** When the person says they're done, the
  employee lines up in front of the head clerk with the notebook. Employees are handled in the order they arrive.
- **Only the head clerk decides, and only once.** They check the notebook against the board and the cabinet as they are
  at that moment. If the name on the board isn't the one at the top of the notebook, if an event added after a number
  read would have changed a decision, or if a card is no longer at the stamp noted, they refuse the whole notebook.
  Otherwise, they have everything written. Either way, the employee tells the person the outcome and goes home, and
  the notebook is done. Refused, the person can start over with a new employee: read again, decide again.
- **Under-clerks who act together.** The head clerk directs under-clerks: one who glues to the board, after the last
  page, a page holding every event of the notebook, numbered in the order they were noted, and one for each card
  touched in the cabinet. They all wait for the order, then act together. No one ever sees a write half done: the
  whole notebook appears at once, or nothing does.
- **An employee with no news goes home.** After a minute without an instruction, the employee throws the notebook away.
  If the power goes out, every notebook is lost, and no one makes a fuss: each person starts their work over.
- **Leaving while the notebook is in line.** Once the employee is in line, the head clerk goes to the end, checks and
  writing alike, even if the person leaves the counter. The person then doesn't know whether the notebook was
  written. Coming back with the number, they learn only that the employee has gone home. To know, they look at the
  board or the cabinet.
- **A sheet that stands on its own.** A person who makes no decision, such as one who keeps cards up to date from the
  board at their own pace, needs no employee. They hand the head clerk a complete sheet: the cards to file, replace,
  or pull out, each with the stamp they saw, and any events, each with what it rests on. The head clerk checks the
  sheet the same way as a notebook. Such a person keeps their place on the board on a card of their own, filed with the
  rest: the cards and the place are written together, or not at all.
- **Emptying drawers.** A person at the counter can ask the head clerk to empty the drawer of one type, or the whole
  cabinet. They wait in line like an employee: the notebooks that arrived before them are written first, and the
  removal applies to everything accepted before it.
- **Turning the board around.** Only in a test hall, a person at the counter can ask for the board to be turned
  around. They wait in line like an employee: the notebooks that arrived before them are written on the visible side,
  then the clerks turn the board around. The side that comes up is blank and carries a new name, and no one can read
  the old side any more. The cabinet is emptied at the same time. An employee whose notebook carries the old name
  throws it away at the next instruction. A person who keeps a place elsewhere sees that the name changed, and starts
  over from zero.
