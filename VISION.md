# Acolyte Vision

This is an overall vision for the project it details the overall behaviours and it is handwritten (NO AI ALLOWED IN THIS FILE).

## Summary

Acolyte is meant to be an easy way to set up a personalized, durable agent (aka
acolyte agent) that has a specific workspace folder to play with, whilst having the capability
to easily access other acolyte agents.

Acolyte is composed of 4 plug-and-play modules:

- Orchestrator: when to use Acolyte. Routine jobs and also quick checks.
- Learner: what to remember and how to learn from past sessions.
- Communicator: how to talk to an acolyte agent using services: CLI, Telegram and
  whatever else is wanted.
- Socializer: how to talk to other acolyte agents that are registered into the
  network.

All the four modules do not modify the core OpenCode agentic engine. It uses
its resources, such as skills, CLI binaries via the shell tool, plugins, OpenCode native tools and API contracts. It builds around OpenCode engine only if needed, otherwise tries to be as OpenCode native as
possible. The reason being is that they have a great open-source product.

We also have a Core module that deals with how to create the agents, how to maintain the server going and how to normalize all the information/data. By its nature, it is not plug and play.

## Core

## Orchestrator

`acolyte cron` should be the single way to set up any kind of fresh jobs.
OpenCode v2 already supports background tasks that run indefinitely, so any
in-session orchestration can be done via that recursively so. "Hey schedule a
ping every 15 minutes until this work is done etc.".

So `acolyte cron` does not have the ability to carry on a given session that the
agent is using directly. But, an external job has the ability to communicate
using the Communicator module.

`acolyte cron` sets up an internal structure and does not depend on any
platform scheduling such as MacOs launchd or Linux cronjobs.

## Learner

The Learner module is all about...you guessed it: learning. It is completely
optional, and the default is to learn nothing about you.
