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

