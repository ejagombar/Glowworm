# Overview & Goals
Building the next generation of open source finance tracking software. An alternative to Firefly III, but much more similar to Snoop, whilst being open source and using bank statements and exports as opposed to a direct connection to the account.
It should have a clean, intuiative UI. It should be smart, using AI for automatic imports, categorisation, and tracking.

# Key Features

## Setup and Confiuration Walkthrough
- When first using the software, the user should be walked through the basic features of the app. 
- It will let the user create the categories that they want their finances to be sorted into.

## AI Assisted importer
- Bank statements come in all sorts of formats. The system should be able to automatically detect the format, maybe using an LLM, and then import the data.
- LLM detected schemas need a way for the user to check that the format is correct. It should show an example of a couple of transactions. If it is not correct, the user should be able to correct the format.
- Once the schema is correct, it should be possible to name and save the schema so it can be used again. The AI should try to name the schema. Next time an import is made, the saved schemas should be checked.
- After import, a human review step lets the user fix the categories Glowworm is unsure about and correct any that are wrong.

## Automatic Transfer Detection
- Detect when transactions are made between users' accounnts. If a transaction of X ammount is sent to a user's own account such as to their savings account. This should be detected.
- These transfers will show as a different type, instead of a payment. They will not show up in stats that would be potentially skewed by a large internal transfer that is not an actual expense.

## Automatic Bill Detection
- Regular payments that are made to the same accounts such as Bills or direct debits should be detected and labelled as such.

## Dynamic Data Viewer
- Instead of just relying on static graphs and tables to view data, it should be possible to query the underlying database to view data in custom formats.
- This would be done with the assistance of an LLM. For example, you may ask a question such as `show how much I have spent on Transport over the past year`. The LLM would then convert that into a query to the database to extract the relevant data. It would show highlights of the thought process, such as detecting that `Transport` is a category, and assumining that we care about all bank accounts, not just a particular account. Using this extracted data, it would display a graph widget showing this data. The graph widget should have a configuration options that allows you to change graph type. Maybe you want a bar chart, or a compounding line graph. It should also be possible to 'pin' these graphs, adding them to a dashboard so they can be referred back to later.

### Dynamic Widgets
- To present the data to the user dynamically, a dynamic widget system is needed. This should be a collection of JSON defined widgets that the LLM can select and use to present data to the user.  
- These widgets could be a bar chart, line chart, pie chart, table, and more simple ones like text and numbers, or a calculator for example.
- The widget JSON should encapsualate the settings of the chart, but they should also be transferrable. For example, the LLM may select a bar chart to present the data, but it should be possible for the end user to modify the settings of said bar chart and perhaps change the column widths, or change it to a line chart (if applicable).
- When respondining to any prompt from a user, one or more dynamic widgets may be used. Simpler widgets like text and numbers should also exist. The text widget would be very simple, and would essentially display the message one to one, but a number widget may display the number in a more stylised fashion, such as putting the number in a box, or in a large font.
- The exact visual specifics of the widgets will not be defined within the widget system. They will be frontend agnostic, meaning that perhaps they will be converted to HTML/CSS counterparts, but they could also use another system such as native QT elements for example.

## Automatic Transaction Categoriser
- When categories are imported, they should be automatically categorised into the availabel categories.
- A fast local classifier (TF-IDF/bag-of-words + linear model, pure Go) handles most transactions offline; the AI model only decides the ones it is unsure about, and a human review step catches the rest.

## Timeline Viewer
- It should be possible to go back in time to any date and see the state of finances at this date. 
- This allow reconciliation, and the ability to look for any divergences that the finance tracker has from reality

## Audit Logger and History
- Every change that is made, either by the user or an AI should be logged. It should also be reversible.
- The log should be a rich log, not just text. It should show exactly what was done.
- It should be possible to navigate the history and return to any point in the history. This means each change should be a 'diff' similar to git.

## AI Agnostic
- It should be possible to customise the AI provider from a number of options. SHould support different API standards. 

# User Interface
- The UI is already defined accuratly in the CSS, JS and HTML files. THis is the final UI. The features within this UI should dictate lots of the features within the softwre we are building
 
# Tech Stack
- Golang backend and core engine.
- Web UI - Perhaps HTMX? Keep it simple. No web UI frameworks such as React.
- Deployed as a website. No mobile app since the web UI should be mobile friendly. Keep the possibility for an App in the future.
- Needs a powerful API for access data, as well as adding transactions.

## Other features
On the transactions page there are a number of issues with the model and with the clarity

For each transaction, they have a source and a destination.
- For a normal transaction, the source is a card and the destination is a merchant. 
- For a transfer, the source is an account, and the destination is another account

When importing two separate bank statements, for example, one from a bank account and one from a credit card, a transfer between the two will show as two separate transactions; one that recieves money, and another that sends money.

On the per account view, we need to be able to see each of these transactions as they appear on the bank / credit card statement. It needs to be clear about the account we are talking about and where the money is going / coming to.

We are also going to do some detection on the backend to link together transfers. This means that on the main list of payments that are not sorted by account, these two transactions that make up the one transfer should appear as one. They should have the linked tag to represent this. 

It may be the case that a transfer is not detected or a transfer is detected incrorrectly. This means that it must be possible to edit a transfer and break the link, and equally, it must be possible to link two transactions together. When linking transactions together, when editing one transaction, if you select transfer type, then it should then let you choose an account and associated transaction, suggesting transactions that are a similar date / amount first.

Recurring transactions is the other tag type. It should not be possible to set this as it will be detected automatically, but it should be possible to click on the recurring tag and see all the related transactions over time.

Another change you should make is that there is an Account dropdown next to the type dropdown and this is not required as you already can select an account at the top.

## Merchants
- Currently, there is a column for Merchants in the transactions table. In reality, a transaction will have a description but not a defined merchant. Also, it is not definite that all transactions have a merchant. For example, it could be an internal transfer, or a transfer to a friend or family so maybe a more generic name is needed.
We should show this description as a column.
We also will use AI to detect the merchant / destination or Source.


Before we start the implementation, here are a couple more requirements:
- The website will be available on the public internet. We need a secure login system. For now, there can only be one account, but we need a username and password login page. Account creation can either be done on this login page, or during the setup of the applications. Pick one.
- With the audit log, it should be possible to jump back to previous points in the history. The actions that were taken after this point should be collapsed down in the UI, still expandable if the user needs to see what happened, but they would no longer be active. It should be possible to restore them too, even after new changes are applied by reverting back to that same point, and applying the original path.
- For some changes like linking transactions, (which may have been done by the AI) it should be possible to delete this change from the audit history, without having to undo all the preceding changes. It should be up to the audit log backend object and audit log engine to decide what is possible for each item
