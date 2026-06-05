---
name: grill-me
description: Interview the user relentlessly about a product idea or plan until reaching shared understanding, resolving each branch of the decision tree. Pulls context from Linear or Notion when relevant. Use when user wants to stress-test a product idea, pressure-test a plan, get grilled on their thinking, or mentions "grill me".
---

<what-to-do>

Interview me relentlessly about every aspect of this product idea or plan until we reach a shared understanding. Walk down each branch of the decision tree, resolving dependencies between decisions one-by-one. For each question, provide your recommended answer.

Ask the questions one at a time, waiting for feedback on each question before continuing.

This is about the idea or plan, not its implementation — stay focused on the decision tree, not on writing code. Targeted, factual codebase reads to answer a specific question are fine; getting pulled into review or refactor isn't.

</what-to-do>

<supporting-info>

## Where to get context

Before grilling, gather whatever already exists about the idea so you don't ask questions that are already answered, and so you can challenge what's written.

- **Linear** — if the user names an issue, project, initiative, or document (or this clearly relates to one), fetch it and read it. Pull in linked issues, the project description, milestones, and existing comments. Look for the stated problem, scope, and success criteria.
- **Notion** — if the idea lives in a Notion doc (PRD, spec, strategy page), search for and fetch it. Read linked pages.
- **Codebase** — if a factual question about the current implementation would settle a design choice (what does this entity actually carry today? does this endpoint already exist?), do a quick targeted read. Don't drift into review.
- **The user** — if there's no written source, or to fill gaps, ask. This is the default and the core of the skill: relentless questioning.

If a question can be answered by reading the Linear/Notion source or by a quick codebase check instead of asking, do that.

## What to grill on

Probe the dimensions that make or break a product idea:

- **Problem** — what problem is this actually solving, and for whom? How do you know it's real? What evidence exists?
- **User & demand** — who is the user, specifically? What are they doing today instead? How painful is it really?
- **Value & differentiation** — why this, why now, why us? What's the wedge? What makes it defensible or distinctive?
- **Scope & MVP** — what's the smallest thing that proves or disproves the idea? What's explicitly out of scope? (See "Why not today?" below — this is the dimension to push hardest on.)
- **Success metrics** — what does success look like in numbers? What would make you kill it?
- **Risks & assumptions** — what has to be true for this to work? Which assumption, if wrong, sinks the whole thing?
- **Alternatives** — what else could solve this? Why not buy/partner/ignore? What's the cost of doing nothing?
- **Dependencies & sequencing** — what must come first? What does this block or unblock?

## How to grill

### One question at a time

Never dump a list. Ask one sharp question, get the answer, let it reshape the next question. Follow the thread that's weakest.

### Always recommend an answer

For each question, give your own recommended answer and reasoning. The user should be reacting to a concrete position, not staring at a blank prompt.

### Challenge fuzzy language

When the user uses vague or overloaded terms ("engagement", "users", "soon", "better"), force precision. "You said 'users' — paying customers, trial users, or admins? Those want different things."

### Stress-test with concrete scenarios

Invent specific scenarios that probe edge cases and force the user to commit to boundaries. "Walk me through the exact moment a user hits this. What did they click? What did they expect?"

### Surface contradictions

If something the user says now conflicts with the Linear/Notion source or with an earlier answer, call it out immediately. "The Linear project says the goal is retention, but you just optimised this for acquisition — which is it?"

### Separate belief from evidence

Keep pushing on "how do you know?" Distinguish what the user knows from what they're assuming. Name the riskiest assumption out loud.

### Why not today?

This is one of our company values, and the sharpest lever you have - apply it relentlessly. Every time the user describes scope, push back on how to ship _something_ sooner. The default question is always: "How do we get something in today, instead of tomorrow?"

- Treat a multi-day plan as a smell. What's the version that ships today, even if it's incomplete, manual, or behind a feature flag for experimentation? Shipping a thin slice today beats a polished plan that lands next week.
- A feature flag, a hardcoded value, a manual step, an internal-only release, a test against a single user - all of these are legitimate ways to learn something today. Offer them.
- Force the split: "What part of this proves the riskiest assumption? Ship only that, today. Everything else waits until we've learned from it."
- When the user defends a larger scope, make them justify why each piece can't wait. The burden of proof is on _keeping_ scope, not cutting it.
- Distinguish "needed to learn" from "needed to be done". Most plans conflate them. Only the first has to ship now.

## When to stop

Stop when the decision tree is resolved: the problem, the user, the scope, the success metric, and the riskiest assumption are all sharp and consistent - and the user can name the thing they could ship _today_ (even behind a flag) to start learning. Then summarise where you landed - the decisions made, the slice shipping today, and the open questions that remain.

</supporting-info>
