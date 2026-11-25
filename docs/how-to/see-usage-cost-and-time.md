---
diataxis: how-to
devices: [mac, iphone, ipad, web]
description: Open the Cost report's Spend, Tokens and Time tabs, read their charts, and see how much of each runtime's allowance is used.
---

# See usage, cost and time

Agent Loops keeps a record of what your agents cost, the tokens they used, and where their
time went. The **Cost** report shows it in three tabs: **Spend**, **Tokens** and **Time**. Use them to
see which days and which runtimes cost the most, and how long agents wait for you.
They only show figures. To set a limit, see [Limit what agents spend](limit-spending.md).

## Open the report

- **Mac:** click **Cost** in the sidebar's **Reports** group, or choose **View ▸ Cost**
  (Option-Command-S). Then choose the tab **Spend**, **Tokens** or **Time**.
- **Web page:** open **Reports ▸ Cost**, then choose a tab. Your own charts are under
  **On your machines**.
- **iPhone and iPad:** they do not have this report.

Hover over the **Cost** row on the Mac to see today's cost and tokens, and what is left of
a daily limit. Near a daily limit, the row's icon turns red. This tells you to look
before the limit stops new work.

## Read the charts

**Spend**, **Tokens** and **Time** draw the same kind of chart, in the same way as the
Health app. The charts add up every host that is connected, in your own day.

1. Choose a range above the chart:
   - **D** is today, by the hour.
   - **W** is the last seven days.
   - **M** is the last thirty days.
   - **3M** is the last thirteen weeks, one bar for each week.
2. Read the figure above the bars. For **D** it is the **Total**. For the other ranges it
   is the **Daily average**, and a dashed line on the chart shows it.
3. To see one bar's figure, click or tap the bar. The other bars go pale. Choose a new
   range to clear it.

Under the bars, each runtime has its share of the range, largest first. Work from before
the record began shows as **Earlier**.

## What Spend shows

The chart shows money, in the currency that most was spent in. Under the chart:

- **All time** is the total for all of the work. Under it, each folder has its share,
  largest first. Archived folders show **Archived** and stay in the list, so the total
  always adds up.
- **At least this much** means that some chats ran on a runtime that reports no price.
  The figure is then a floor, not the full cost.
- **Also spent in** names other currencies. The chart does not draw them, because the app
  does not convert between currencies.

On the iPhone, iPad and web page, the page also shows **Today**, and each host's daily
limit and limit for each agent, with what is left. You set limits only on the Mac, in
**Settings ▸ Cost**.

On the Mac, **Open Reports** opens the same report on the web page, from what each machine
sends to the control plane. See [Reports](../reference/reports.md).

## What Tokens shows

The chart shows the fresh tokens of each turn: input, output, thought, and tokens written
to a cache. Tokens that the model read back from a cache are not in the bars, because
they are most of every total and would hide the rest. The page gives them as one line:
**Plus … read from cache, not drawn.**

Under the chart are the tokens used today, the daily token limit and what is left, and
the token limit for each agent. On the Mac this is under **Today**. On the iPhone, iPad
and web page, each host has its own lines.

## What Time shows

The chart shows where the time of running chats went. Each bar has four parts, one for
each kind of time:

- **Agent**: the agent thinks or writes.
- **Tools**: a tool call is open.
- **Waiting on others**: the agent waits on something it does not control, such as
  `wait_for_event` or `lease_resource`.
- **Waiting for you**: a question, a permission or a form waits for your answer.

When kinds overlap, **Waiting for you** counts first, then **Waiting on others**, then
**Tools**. Under the chart, each kind has its time and its share. With more than one
runtime, **By runtime** gives each runtime's time.

Chats run side by side, so one hour can hold more than one hour of chat time. The time
between turns is not counted. A large **Waiting for you** share tells you that agents
spend time on your answers. You can then answer sooner, or give them more to do in each
prompt.

The page loads new figures every minute while it is open. When you have earned every
milestone, the **Milestones** list is under the Time charts. See
[Achievements](../reference/achievements.md).

## See how much of an allowance is used

A runtime on a plan, such as a Claude or Grok subscription, has an allowance for a
period of time. The **Runtimes** page, in the **Your setup** group of the sidebar, shows
where each allowance stands. When the runtime gives an amount, a bar under the words
shows how much is used:

- The bar is in the accent colour while there is enough left.
- It turns orange when the allowance is nearly spent, or 80% or more is used.
- It turns red when the allowance is spent.

The bar is the same on the Mac, the iPhone and iPad, and the web page. When a runtime
gives only a reset time, there is no bar. On the iPhone and iPad, **Runtimes** is also
at the foot of the **Cost** page.

## If it doesn't work

- **A chart says *Nothing was spent in this range*.** No runtime reported a price in
  that range. Choose a longer range, or open **Tokens**.
- **The Cost figure seems low.** Look for **At least this much**. Some runtimes report
  tokens but no price. See [Runtimes](../reference/runtimes.md).
- **Time says *No chat ran in this range*.** No turn ran in that range. Time between
  turns is not counted.

## See also

- [Limit what agents spend](limit-spending.md)
- [Keep going when a runtime runs out](keep-going-when-a-runtime-runs-out.md)
- [Reports](../reference/reports.md)
- [Runtimes](../reference/runtimes.md)
