# UI/UX audit

The original dashboard split navigation between unrelated visual systems and its
Runs page depended on legacy endpoints unavailable from `prizm serve`, producing
an empty primary screen. The overhaul makes the live API-backed overview the
default, retains existing editor workflows, and presents pending approvals and
failed tasks before secondary data.
