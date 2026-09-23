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
- The UI should be modern yet professional. Think Hargreaves Lansdown App / Website UI, similar to that but do not copy.
- It should prioritise UX over looks. A UI that a professional UI/UX designer from the likes of Apple would be proud of.
 
# Tech Stack
- Golang backend and core engine.
- Web UI - Perhaps HTMX? Keep it simple. No web UI frameworks such as React.
- Deployed as a website. No mobile app since the web UI should be mobile friendly. Keep the possibility for an App in the future.
- Needs a powerful API for access data, as well as adding transactions.
